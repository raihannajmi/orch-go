package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// run.json is the durable state of one `orch build` run. It is written
// atomically after every meaningful stage transition so an interrupted run can
// be resumed from its first incomplete stage. The numbered .md/.log/.timeout
// artifacts stay the source of truth for whether a stage finished; run.json
// records the things the artifacts alone cannot express: the task text, the
// repository, the progress cursor and the terminal outcome.
const (
	runStateFile     = "run.json"
	runLockFile      = "run.lock"
	workflowLockFile = "workflow.lock"
	runStateVersion  = 1
)

// Run-level states. running is the only non-terminal one; interrupted is derived
// when a run claims to be running but no live process holds its lock.
const (
	runStateRunning     = "running"
	runStateCompleted   = "completed"
	runStateFailed      = "failed"
	runStateTimedOut    = "timed_out"
	runStateInterrupted = "interrupted"
)

// Per-stage states, mirroring the artifact protocol on disk.
const (
	stageRunning   = "running"
	stageCompleted = "completed"
	stageTimedOut  = "timed_out"
	stageFailed    = "failed"
)

// Verification outcomes, persisted explicitly because verify.log has no marker
// of its own.
const (
	verifyPassed = "passed"
	verifyFailed = "failed"
)

// Failure kinds recorded in run.json (and surfaced by --json), so a consumer can
// tell an agent failure from orch's own errors and from a watchdog timeout or a
// failed verification.
const (
	failureAgent    = "agent"    // an agent process exited non-zero
	failureTimeout  = "timeout"  // the per-stage watchdog ended the stage
	failureVerify   = "verify"   // repository verification failed
	failureState    = "state"    // run.json could not be persisted
	failureWorkflow = "workflow" // any other workflow failure
)

// errRunLocked reports that another orch process already owns the run.
var errRunLocked = errors.New("run is already in progress")

// errStateWrite marks a failure to persist run.json. It is fatal: continuing
// would lose the recovery state a later resume depends on, and resume must never
// see a stage recorded as complete when it was not written.
var errStateWrite = errors.New("could not persist run state")

// classifyFailure maps a workflow error to a failure kind and, when an agent
// exited non-zero, to that agent's exit code. It keeps the distinctions the CLI
// and --json report from collapsing into one opaque failure.
func classifyFailure(err error) (kind string, code int) {
	var ae *agentExitError
	switch {
	case err == nil:
		return "", 0
	case errors.As(err, &ae):
		return failureAgent, ae.Code
	case errors.Is(err, errStageTimeout):
		return failureTimeout, 0
	case errors.Is(err, errVerifyFailed):
		return failureVerify, 0
	case errors.Is(err, errStateWrite):
		return failureState, 0
	default:
		return failureWorkflow, 0
	}
}

// stageState is one stage of the workflow as recorded in run.json.
type stageState struct {
	Name     string `json:"name"`     // numbered stage, e.g. "3-implement"
	Agent    string `json:"agent"`    // agent that owns the stage, e.g. "agy"
	Artifact string `json:"artifact"` // Markdown artifact the stage writes
	Status   string `json:"status"`   // running | completed | timed_out | failed
}

// runState is the persisted state of one run.
type runState struct {
	Version       int          `json:"version"`
	ID            string       `json:"id"`
	Task          string       `json:"task"`
	RepoDir       string       `json:"repoDir"`
	CreatedAt     string       `json:"createdAt"`
	StageTimeout  string       `json:"stageTimeout"`
	Knowledge     bool         `json:"knowledge"`
	Status        string       `json:"status"`
	Stages        []stageState `json:"stages"`
	LastCompleted int          `json:"lastCompleted"`
	BaseCommit    string       `json:"baseCommit"`
	Verify        []verifySpec `json:"verify,omitempty"`
	VerifyResult  string       `json:"verifyResult,omitempty"`
	Failure       string       `json:"failure,omitempty"`
	ExitCode      int          `json:"exitCode,omitempty"`
	Error         string       `json:"error,omitempty"`
}

// writeRunState writes run.json atomically: a temp file in the run directory is
// renamed over the real one, so a reader never sees a half-written state and a
// crash mid-write leaves the previous state intact.
func writeRunState(runDir string, st runState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", runStateFile, err)
	}
	b = append(b, '\n')

	tmp, err := os.CreateTemp(runDir, runStateFile+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// CreateTemp already opens 0600; set it explicitly so the state file's mode
	// does not depend on the platform's default.
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(runDir, runStateFile)); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// readRunState loads run.json, returning fs.ErrNotExist for a run that predates
// resume support, so callers can tell "legacy" from "broken".
func readRunState(runDir string) (runState, error) {
	b, err := os.ReadFile(filepath.Join(runDir, runStateFile))
	if err != nil {
		return runState{}, err
	}
	var st runState
	if err := json.Unmarshal(b, &st); err != nil {
		return runState{}, fmt.Errorf("parse %s: %w", runStateFile, err)
	}
	return st, nil
}

// ensureStateDir creates the .orch state directory with owner-only
// permissions. It never tightens an existing directory, so pre-existing
// artifacts keep whatever access they already had.
func ensureStateDir(stateDir string) error {
	return os.MkdirAll(stateDir, 0o700)
}

// createRunDir makes a fresh run directory under stateDir. The id keeps the
// timestamp form so existing ids and `orch status` ordering are unchanged, but a
// same-second collision gets a numeric suffix instead of silently sharing a
// directory: Mkdir fails on an existing name, which the old MkdirAll masked.
// New run directories are owner-only; existing ones are left untouched.
func createRunDir(stateDir string) (id, dir string, err error) {
	if err := ensureStateDir(stateDir); err != nil {
		return "", "", err
	}
	base := time.Now().UTC().Format("20060102-150405")
	for i := 0; i < 1000; i++ {
		id = base
		if i > 0 {
			id = fmt.Sprintf("%s-%d", base, i+1)
		}
		dir = filepath.Join(stateDir, id)
		mkErr := os.Mkdir(dir, 0o700)
		if mkErr == nil {
			return id, dir, nil
		}
		if !errors.Is(mkErr, fs.ErrExist) {
			return "", "", mkErr
		}
	}
	return "", "", fmt.Errorf("could not create a unique run directory named %s", base)
}

// runLock is an exclusive advisory lock backed by flock, which the kernel
// releases when the owning process dies. A stale lock therefore never blocks a
// later run: a free per-run lock on a run whose state says "running" is exactly
// how an interrupted run is detected, and the repository lock is released even
// when a build is killed or cancelled.
type runLock struct{ f *os.File }

// lockPath takes an exclusive, non-blocking flock on path, creating the lock
// file when needed. It returns errRunLocked when another process already holds
// it. flock is used rather than a PID file precisely because the kernel releases
// it on process exit, so a crashed or killed process cannot leave a lock behind.
func lockPath(path string) (*runLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errRunLocked
		}
		return nil, err
	}
	return &runLock{f: f}, nil
}

// lockRun takes the run's exclusive lock without blocking. It returns
// errRunLocked when another process holds it.
func lockRun(runDir string) (*runLock, error) {
	return lockPath(filepath.Join(runDir, runLockFile))
}

// lockWorkflow takes the repository-level lock that lets only one mutating
// workflow (build or resume) operate on a repository at a time. It lives under
// the repository's .orch directory and returns errRunLocked when another
// workflow already holds it.
func lockWorkflow(stateDir string) (*runLock, error) {
	return lockPath(filepath.Join(stateDir, workflowLockFile))
}

// Close releases the lock.
func (l *runLock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	if err := unix.Flock(int(l.f.Fd()), unix.LOCK_UN); err != nil {
		_ = l.f.Close()
		return err
	}
	return l.f.Close()
}

// gitHead returns the current HEAD commit, or an error when dir is not a git
// work tree (or git is unavailable).
func gitHead(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitBaseCommit is the best-effort HEAD recorded when a run starts. No git
// repository simply yields "", and the run still proceeds.
func gitBaseCommit(dir string) string {
	head, err := gitHead(dir)
	if err != nil {
		return ""
	}
	return head
}

// gitStatus returns porcelain status with the run's own state directory filtered
// out: .orch is orch's bookkeeping, not a user change, and the repository being
// built is not guaranteed to gitignore it.
func gitStatus(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return "", err
	}
	var kept []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		if len(line) > 3 {
			path := strings.TrimSpace(line[3:])
			if path == buildStateDir || strings.HasPrefix(path, buildStateDir+"/") {
				continue
			}
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n"), nil
}

// checkResumeSafe decides whether the repository can be resumed without risking
// the user's work. It never mutates the repository — orch does not reset,
// checkout, clean or stash. When the state is unsafe it refuses; when it is
// merely suspicious (uncommitted changes left by the interrupted stage) it
// returns a warning so the caller can proceed without pretending the tree is
// pristine.
func checkResumeSafe(repoDir, baseCommit string, mutated bool) (warning string, err error) {
	if baseCommit == "" {
		return "", errors.New("no base commit was recorded (the repository was not a git work tree when the run started); refusing to resume")
	}
	head, err := gitHead(repoDir)
	if err != nil {
		return "", fmt.Errorf("cannot read git HEAD in %s: %w", repoDir, err)
	}
	if head != baseCommit {
		return "", fmt.Errorf("repository HEAD moved since the run started (base %s, now %s); refusing to resume so your commits are not disturbed", shortHash(baseCommit), shortHash(head))
	}
	status, err := gitStatus(repoDir)
	if err != nil {
		return "", fmt.Errorf("cannot read git status in %s: %w", repoDir, err)
	}
	if strings.TrimSpace(status) == "" {
		return "", nil
	}
	if !mutated {
		return "", errors.New("the working tree has uncommitted changes but no stage of this run has modified the repository; refusing to resume on an unexpectedly dirty tree so your changes are not disturbed")
	}
	return "working tree still holds uncommitted changes from the interrupted stage; they are not rolled back and the re-run stage will build on them", nil
}

// shortHash abbreviates a commit id for error messages.
func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// mutatingStage reports whether a stage edits the repository (implement/fix), as
// opposed to the read-only plan/review stages.
func mutatingStage(name string) bool {
	return strings.HasSuffix(name, "-implement") || strings.HasSuffix(name, "-fix")
}

// repoMutatedByRun reports whether any recorded stage of the run could have
// changed the working tree. An incomplete implement stage counts: it may have
// written files before it was interrupted.
func repoMutatedByRun(st runState) bool {
	for _, s := range st.Stages {
		if mutatingStage(s.Name) {
			return true
		}
	}
	return false
}

// firstIncompleteStage returns the index of the first stage whose artifact does
// not end with the completion marker — the stage a resume must re-run. Stages
// that pass the marker test are complete by the artifact protocol whatever
// run.json says, so a stage whose artifact was lost is re-run rather than
// trusted from the manifest alone.
func firstIncompleteStage(runDir string, st runState) int {
	for i := range st.Stages {
		if artifactComplete(filepath.Join(runDir, st.Stages[i].Artifact)) {
			continue
		}
		return i
	}
	return len(st.Stages)
}

// formatStageTimeout renders the watchdog duration for run.json; "disabled"
// records that the watchdog was off.
func formatStageTimeout(d time.Duration) string {
	if d <= 0 {
		return "disabled"
	}
	return d.String()
}

// parseStateTimeout reads the watchdog duration back. A corrupt value falls back
// to the default rather than silently disabling the watchdog.
func parseStateTimeout(s string) time.Duration {
	if s == "" || s == "disabled" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return defaultStageTimeout
	}
	return d
}
