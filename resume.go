package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// cmdResume resumes an interrupted or failed `orch build` run from its first
// incomplete stage. It is deliberately conservative: it refuses to touch a
// repository it cannot prove is safe, and it never rolls back the partial
// changes an interrupted stage may have left behind. Every resumed stage is a
// normal interactive session with native permission prompts — orch adds no
// bypass flags and runs nothing headless.
func cmdResume(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	asJSON := jsonRequested(args)
	var id string
	for _, arg := range args {
		switch {
		case arg == "-h" || arg == "--help":
			usage(stdout)
			return 0
		case arg == "--json":
		case strings.HasPrefix(arg, "-") && arg != "-":
			return cliError("resume", asJSON, stdout, stderr, 2, "unknown flag %q for resume", arg)
		case id == "":
			id = arg
		default:
			return cliError("resume", asJSON, stdout, stderr, 2, "resume takes a single run id")
		}
	}
	if id == "" {
		return cliError("resume", asJSON, stdout, stderr, 2, "resume needs a run id, e.g. orch resume 20260101-120000")
	}
	if err := validRunID(id); err != nil {
		return cliError("resume", asJSON, stdout, stderr, 2, "%v", err)
	}

	runDir := filepath.Join(buildStateDir, id)
	if info, err := os.Stat(runDir); err != nil || !info.IsDir() {
		return cliError("resume", asJSON, stdout, stderr, 2, "unknown run %q (see `orch status`)", id)
	}

	st, err := readRunState(runDir)
	if errors.Is(err, fs.ErrNotExist) {
		return cliError("resume", asJSON, stdout, stderr, 2, "run %s predates resume support (no %s); it stays readable with `orch status`/`orch logs` but cannot be resumed", id, runStateFile)
	}
	if err != nil {
		return cliError("resume", asJSON, stdout, stderr, 1, "%v", err)
	}

	// Serialize whole workflows per repository, exactly as `orch build` does, and
	// before the per-run lock. Taking them in this order everywhere (workflow
	// then run) means a build and a resume can never deadlock against each other.
	stateDir := filepath.Dir(runDir)
	workflowLock, err := lockWorkflow(stateDir)
	if errors.Is(err, errRunLocked) {
		return cliError("resume", asJSON, stdout, stderr, 2, "another orch workflow is already running in this repository; refusing to resume %s concurrently", id)
	}
	if err != nil {
		return cliError("resume", asJSON, stdout, stderr, 1, "%v", err)
	}
	defer func() { _ = workflowLock.Close() }()

	// Lock the run itself before anything else: a live run must never be resumed
	// underneath its owner. A free lock on a run whose state says "running" is
	// proof the previous process died, i.e. the run was interrupted.
	lock, err := lockRun(runDir)
	if errors.Is(err, errRunLocked) {
		return cliError("resume", asJSON, stdout, stderr, 2, "run %s is already in progress; refusing to resume it concurrently", id)
	}
	if err != nil {
		return cliError("resume", asJSON, stdout, stderr, 1, "%v", err)
	}
	defer func() { _ = lock.Close() }()

	if st.Status == runStateCompleted {
		if asJSON {
			if err := emitJSON(stdout, newRunResponse("resume", runDir, &st, 0, nil)); err != nil {
				fmt.Fprintf(stderr, "orch: %v\n", err)
				return 1
			}
			return 0
		}
		fmt.Fprintf(stdout, "orch: run %s is already complete; nothing to resume\n", id)
		return 0
	}
	if st.Status == runStateRunning {
		st.Status = runStateInterrupted
		st.Error = "interrupted: the orchestrating process ended mid-run"
		if err := writeRunState(runDir, st); err != nil {
			return cliError("resume", asJSON, stdout, stderr, 1, "%v", err)
		}
		fmt.Fprintf(stderr, "orch: run %s was interrupted; resuming from the first incomplete stage\n", id)
	}

	// Resume from the first stage whose artifact is not complete by the marker
	// protocol. A run whose stages are all complete but whose verification failed
	// resumes by re-running verification.
	resumeFrom := firstIncompleteStage(runDir, st)
	if resumeFrom == len(st.Stages) && st.VerifyResult != verifyFailed && len(st.Stages) > 0 {
		if st.Error != "" {
			return cliError("resume", asJSON, stdout, stderr, 1, "run %s has no incomplete stage to resume (status %s: %s)", id, st.Status, st.Error)
		}
		return cliError("resume", asJSON, stdout, stderr, 1, "run %s has no incomplete stage to resume (status %s)", id, st.Status)
	}

	// The repository must be safe to continue in. orch never resets, checks out
	// or cleans: it refuses instead of disturbing the user's work.
	if warn, err := checkResumeSafe(st.RepoDir, st.BaseCommit, repoMutatedByRun(st)); err != nil {
		return cliError("resume", asJSON, stdout, stderr, 1, "cannot resume run %s: %v", id, err)
	} else if warn != "" {
		fmt.Fprintf(stderr, "orch: warning: %s\n", warn)
	}

	// Everything from the first incomplete stage onward is re-run, so the recorded
	// tail is discarded and rebuilt as the workflow proceeds. Completed stages
	// before it are replayed from disk, never re-launched.
	st.Stages = st.Stages[:resumeFrom]

	// Verify with the commands the run was created with, so a resume checks the
	// same thing the original run would have.
	verifySpecs, err := resumeVerifySpecs(st)
	if err != nil {
		return cliError("resume", asJSON, stdout, stderr, 1, "cannot resume run %s: %v", id, err)
	}

	// Under --json the interactive agents' terminal output goes to stderr, so
	// stdout carries only the final JSON document.
	agentOut := stdout
	if asJSON {
		agentOut = stderr
	}

	b := &builder{
		dir:         st.RepoDir,
		runDir:      runDir,
		stdout:      agentOut,
		stderr:      stderr,
		stage:       interactiveStage(st.RepoDir, runDir, stdin, agentOut, parseStateTimeout(st.StageTimeout)),
		state:       &st,
		resumeFrom:  resumeFrom,
		verifySpecs: verifySpecs,
	}
	b.verify = b.verifyRepo
	if b.knowledge = openKnowledge(st.Knowledge, st.RepoDir, b.logf); b.knowledge != nil {
		defer b.knowledge.close()
	}

	st.Status = runStateRunning
	st.Error = ""
	if err := writeRunState(runDir, st); err != nil {
		return cliError("resume", asJSON, stdout, stderr, 1, "%v", err)
	}

	buildErr := b.build(st.Task)
	if asJSON {
		code := 0
		if buildErr != nil {
			code = 1
		}
		if err := emitJSON(stdout, newRunResponse("resume", runDir, &st, code, buildErr)); err != nil {
			fmt.Fprintf(stderr, "orch: %v\n", err)
			return 1
		}
		return code
	}
	if buildErr != nil {
		fmt.Fprintf(stderr, "orch: resume: %v\n", buildErr)
		return 1
	}
	fmt.Fprintf(stdout, "orch: resumed run %s to completion; artifacts in %s\n", id, runDir)
	return 0
}
