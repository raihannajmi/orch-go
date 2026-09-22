package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// standInSuccessScript writes the artifact named in the stage prompt (the only
// absolute *.md path the prompt contains), approves reviews, and ends with the
// completion marker, so a full workflow can run without a real agent.
const standInSuccessScript = `#!/bin/sh
out=$(printf '%s\n' "$@" | grep -m1 -E '^/[^[:space:]]*\.md$')
[ -n "$out" ] || exit 3
{
  echo "stand-in output"
  case "$out" in
    *-review.md) echo "VERDICT: APPROVED" ;;
  esac
  echo "ORCH_STAGE_COMPLETE"
} > "$out"
`

// installStandInAgents puts stand-in agy and command-code executables in a temp
// directory and returns it. The caller prepends it to PATH.
func installStandInAgents(t *testing.T, script string) string {
	t.Helper()
	if script == "" {
		script = standInSuccessScript
	}
	binDir := t.TempDir()
	for _, name := range []string{buildPlanAgent, buildReviewAgent} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0o755); err != nil {
			t.Fatalf("install %s: %v", name, err)
		}
	}
	return binDir
}

// newGoRepo creates a tiny, gofmt-clean Go module so auto-detected verification
// passes. It skips when the Go toolchain is unavailable.
func newGoRepo(t *testing.T) string {
	t.Helper()
	for _, bin := range []string{"go", "gofmt"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not available", bin)
		}
	}
	repo := t.TempDir()
	writeFiles(t, repo, map[string]string{
		"go.mod":       "module example.com/standin\n\ngo 1.25\n",
		"main.go":      "package main\n\nfunc main() {}\n",
		"main_test.go": "package main\n\nimport \"testing\"\n\nfunc TestOK(t *testing.T) {}\n",
	})
	return repo
}

// standInPATH makes the stand-in agents discoverable while leaving the real
// tools (go, gofmt, git) on PATH for verification and repo checks.
func standInPATH(t *testing.T, binDir string) {
	t.Helper()
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %o, want %o", path, got, want)
	}
}

// TestWorkflowLockExclusiveAndReleased covers the primitive: the repository lock
// is exclusive, and releasing it (what Close does on success or failure, and
// what the kernel does when the process dies) lets the next workflow in.
func TestWorkflowLockExclusiveAndReleased(t *testing.T) {
	dir := t.TempDir()

	first, err := lockWorkflow(dir)
	if err != nil {
		t.Fatalf("lockWorkflow: %v", err)
	}
	if _, err := lockWorkflow(dir); !errors.Is(err, errRunLocked) {
		t.Fatalf("second lockWorkflow = %v, want errRunLocked", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("release: %v", err)
	}
	again, err := lockWorkflow(dir)
	if err != nil {
		t.Fatalf("lockWorkflow after release: %v", err)
	}
	_ = again.Close()
}

// TestIndependentRepositoriesLockIndependently covers requirement 6: locks are
// per .orch directory, so two repositories never block each other.
func TestIndependentRepositoriesLockIndependently(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()

	la, err := lockWorkflow(a)
	if err != nil {
		t.Fatalf("lock a: %v", err)
	}
	defer func() { _ = la.Close() }()

	lb, err := lockWorkflow(b)
	if err != nil {
		t.Fatalf("independent repository b was blocked: %v", err)
	}
	_ = lb.Close()
}

// TestConcurrentBuildsSameRepoRefused simulates a second build while a workflow
// holds the repository lock: it must fail cleanly and create no run state.
func TestConcurrentBuildsSameRepoRefused(t *testing.T) {
	repo := newGoRepo(t)
	standInPATH(t, installStandInAgents(t, ""))

	stateDir := filepath.Join(repo, buildStateDir)
	if err := ensureStateDir(stateDir); err != nil {
		t.Fatalf("ensureStateDir: %v", err)
	}
	held, err := lockWorkflow(stateDir)
	if err != nil {
		t.Fatalf("lockWorkflow: %v", err)
	}
	defer func() { _ = held.Close() }()

	var out, errb bytes.Buffer
	if code := orchMain([]string{"build", "-C", repo, "task"}, nil, &out, &errb); code != 2 {
		t.Fatalf("build = %d, want 2 (stderr: %s)", code, errb.String())
	}
	if !strings.Contains(errb.String(), "another orch workflow") {
		t.Errorf("stderr = %q, want a clear busy message", errb.String())
	}

	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("read state dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("a refused build created run state: %s", e.Name())
		}
	}
}

// TestBuildVsResumeCollision covers a resume while a build (or another workflow)
// holds the repository lock: resume must refuse.
func TestBuildVsResumeCollision(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeState(t, dir, "20260101-120000", runState{
		Version: runStateVersion, ID: "20260101-120000", Task: "t", RepoDir: dir,
		Status: runStateRunning,
		Stages: []stageState{{Name: "1-plan", Agent: "agy", Artifact: "1-plan.md", Status: stageRunning}},
	})

	stateDir := filepath.Join(dir, buildStateDir)
	held, err := lockWorkflow(stateDir)
	if err != nil {
		t.Fatalf("lockWorkflow: %v", err)
	}
	defer func() { _ = held.Close() }()

	var out, errb bytes.Buffer
	if code := cmdResume([]string{"20260101-120000"}, nil, &out, &errb); code != 2 {
		t.Fatalf("resume = %d, want 2 (stderr: %s)", code, errb.String())
	}
	if !strings.Contains(errb.String(), "another orch workflow") {
		t.Errorf("stderr = %q, want a clear busy message", errb.String())
	}
}

// TestWorkflowLockReleasedAfterSuccess runs a full stand-in workflow and checks
// the repository lock is free afterwards.
func TestWorkflowLockReleasedAfterSuccess(t *testing.T) {
	repo := newGoRepo(t)
	standInPATH(t, installStandInAgents(t, ""))

	var out, errb bytes.Buffer
	if code := orchMain([]string{"build", "-C", repo, "stand-in task"}, nil, &out, &errb); code != 0 {
		t.Fatalf("build = %d, want 0 (stderr: %s)", code, errb.String())
	}

	lock, err := lockWorkflow(filepath.Join(repo, buildStateDir))
	if err != nil {
		t.Fatalf("workflow lock still held after success: %v", err)
	}
	_ = lock.Close()
}

// TestWorkflowLockReleasedAfterFailure runs a workflow whose first stage fails
// and checks the repository lock is still released.
func TestWorkflowLockReleasedAfterFailure(t *testing.T) {
	repo := newGoRepo(t)
	standInPATH(t, installStandInAgents(t, "#!/bin/sh\nexit 1\n"))

	var out, errb bytes.Buffer
	if code := orchMain([]string{"build", "-C", repo, "task"}, nil, &out, &errb); code == 0 {
		t.Fatalf("build = 0, want a failure (stderr: %s)", errb.String())
	}

	lock, err := lockWorkflow(filepath.Join(repo, buildStateDir))
	if err != nil {
		t.Fatalf("workflow lock still held after failure: %v", err)
	}
	_ = lock.Close()
}

// TestWorkflowLockHelperProcess is not a test: re-executed by the kill test, it
// takes the repository lock and blocks until killed.
func TestWorkflowLockHelperProcess(t *testing.T) {
	if os.Getenv("ORCH_LOCK_HELPER") != "1" {
		t.Skip("helper process")
	}
	if _, err := lockWorkflow(os.Getenv("ORCH_LOCK_DIR")); err != nil {
		fmt.Fprintf(os.Stderr, "helper could not acquire the lock: %v\n", err)
		os.Exit(3)
	}
	fmt.Println("locked")
	select {}
}

// TestWorkflowLockReleasedOnProcessKill covers cancellation: a workflow that is
// killed cannot hold the repository lock forever, because the OS releases the
// flock when the owning process dies.
func TestWorkflowLockReleasedOnProcessKill(t *testing.T) {
	dir := t.TempDir()

	cmd := exec.Command(os.Args[0], "-test.run=TestWorkflowLockHelperProcess", "-test.v")
	cmd.Env = append(os.Environ(), "ORCH_LOCK_HELPER=1", "ORCH_LOCK_DIR="+dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	locked := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if strings.Contains(sc.Text(), "locked") {
				close(locked)
				return
			}
		}
	}()
	select {
	case <-locked:
	case <-time.After(20 * time.Second):
		t.Fatal("helper process did not acquire the lock")
	}

	if _, err := lockWorkflow(dir); !errors.Is(err, errRunLocked) {
		t.Fatalf("lock not held by the helper: %v", err)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	_, _ = cmd.Process.Wait()

	deadline := time.Now().Add(10 * time.Second)
	for {
		l, err := lockWorkflow(dir)
		if err == nil {
			_ = l.Close()
			return
		}
		if !errors.Is(err, errRunLocked) {
			t.Fatalf("lockWorkflow: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("the lock was not released after the holder was killed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRuntimeArtifactsArePrivate runs a full stand-in workflow and checks the
// .orch directory and its files are owner-only.
func TestRuntimeArtifactsArePrivate(t *testing.T) {
	repo := newGoRepo(t)
	standInPATH(t, installStandInAgents(t, ""))

	var out, errb bytes.Buffer
	if code := orchMain([]string{"build", "-C", repo, "stand-in task"}, nil, &out, &errb); code != 0 {
		t.Fatalf("build = %d, want 0 (stderr: %s)", code, errb.String())
	}

	stateDir := filepath.Join(repo, buildStateDir)
	assertMode(t, stateDir, 0o700)
	assertMode(t, filepath.Join(stateDir, workflowLockFile), 0o600)

	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("read state dir: %v", err)
	}
	var runs int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		runs++
		runDir := filepath.Join(stateDir, e.Name())
		assertMode(t, runDir, 0o700)
		files, err := os.ReadDir(runDir)
		if err != nil {
			t.Fatalf("read run dir: %v", err)
		}
		for _, f := range files {
			info, err := f.Info()
			if err != nil {
				t.Fatalf("stat %s: %v", f.Name(), err)
			}
			if perm := info.Mode().Perm(); perm != 0o600 {
				t.Errorf("%s mode = %o, want 600", filepath.Join(runDir, f.Name()), perm)
			}
		}
	}
	if runs != 1 {
		t.Errorf("run directories = %d, want 1", runs)
	}
}

// TestLegacyArtifactsRemainReadable covers backward compatibility: runs created
// with broader permissions (or before run.json) are still read by status/logs.
func TestLegacyArtifactsRemainReadable(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	runDir := filepath.Join(dir, buildStateDir, "20260101-120000")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Make the legacy permissions explicit regardless of umask.
	if err := os.Chmod(runDir, 0o755); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	writeArtifact(t, runDir, "1-plan.md", "plan")
	if err := os.Chmod(filepath.Join(runDir, "1-plan.md"), 0o644); err != nil {
		t.Fatalf("chmod file: %v", err)
	}

	var out, errb bytes.Buffer
	if code := cmdStatus(nil, &out, &errb); code != 0 {
		t.Fatalf("status = %d (stderr: %s)", code, errb.String())
	}
	if !strings.Contains(out.String(), "20260101-120000") {
		t.Errorf("status output missing the legacy run:\n%s", out.String())
	}

	out.Reset()
	if code := cmdLogs([]string{"20260101-120000"}, &out, &errb); code != 0 {
		t.Fatalf("logs = %d (stderr: %s)", code, errb.String())
	}
	if !strings.Contains(out.String(), "1-plan.md") {
		t.Errorf("logs output missing the legacy artifact:\n%s", out.String())
	}
}
