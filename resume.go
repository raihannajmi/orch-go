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
	var id string
	for _, arg := range args {
		switch {
		case arg == "-h" || arg == "--help":
			usage(stdout)
			return 0
		case strings.HasPrefix(arg, "-") && arg != "-":
			fmt.Fprintf(stderr, "orch: unknown flag %q for resume\n", arg)
			return 2
		case id == "":
			id = arg
		default:
			fmt.Fprintln(stderr, "orch: resume takes a single run id")
			return 2
		}
	}
	if id == "" {
		fmt.Fprintln(stderr, "orch: resume needs a run id, e.g. orch resume 20260101-120000")
		return 2
	}
	if err := validRunID(id); err != nil {
		fmt.Fprintf(stderr, "orch: %v\n", err)
		return 2
	}

	runDir := filepath.Join(buildStateDir, id)
	if info, err := os.Stat(runDir); err != nil || !info.IsDir() {
		fmt.Fprintf(stderr, "orch: unknown run %q (see `orch status`)\n", id)
		return 2
	}

	st, err := readRunState(runDir)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(stderr, "orch: run %s predates resume support (no %s); it stays readable with `orch status`/`orch logs` but cannot be resumed\n", id, runStateFile)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "orch: %v\n", err)
		return 1
	}

	// Lock before anything else: a live run must never be resumed underneath its
	// owner. A free lock on a run whose state says "running" is proof the previous
	// process died, i.e. the run was interrupted.
	lock, err := lockRun(runDir)
	if errors.Is(err, errRunLocked) {
		fmt.Fprintf(stderr, "orch: run %s is already in progress; refusing to resume it concurrently\n", id)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "orch: %v\n", err)
		return 1
	}
	defer func() { _ = lock.Close() }()

	if st.Status == runStateCompleted {
		fmt.Fprintf(stdout, "orch: run %s is already complete; nothing to resume\n", id)
		return 0
	}
	if st.Status == runStateRunning {
		st.Status = runStateInterrupted
		st.Error = "interrupted: the orchestrating process ended mid-run"
		if err := writeRunState(runDir, st); err != nil {
			fmt.Fprintf(stderr, "orch: %v\n", err)
			return 1
		}
		fmt.Fprintf(stderr, "orch: run %s was interrupted; resuming from the first incomplete stage\n", id)
	}

	// Resume from the first stage whose artifact is not complete by the marker
	// protocol. A run whose stages are all complete but whose verification failed
	// resumes by re-running verification.
	resumeFrom := firstIncompleteStage(runDir, st)
	if resumeFrom == len(st.Stages) && st.VerifyResult != verifyFailed && len(st.Stages) > 0 {
		if st.Error != "" {
			fmt.Fprintf(stderr, "orch: run %s has no incomplete stage to resume (status %s: %s)\n", id, st.Status, st.Error)
		} else {
			fmt.Fprintf(stderr, "orch: run %s has no incomplete stage to resume (status %s)\n", id, st.Status)
		}
		return 1
	}

	// The repository must be safe to continue in. orch never resets, checks out
	// or cleans: it refuses instead of disturbing the user's work.
	if warn, err := checkResumeSafe(st.RepoDir, st.BaseCommit, repoMutatedByRun(st)); err != nil {
		fmt.Fprintf(stderr, "orch: cannot resume run %s: %v\n", id, err)
		return 1
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
		fmt.Fprintf(stderr, "orch: cannot resume run %s: %v\n", id, err)
		return 1
	}

	b := &builder{
		dir:         st.RepoDir,
		runDir:      runDir,
		stdout:      stdout,
		stderr:      stderr,
		stage:       interactiveStage(st.RepoDir, runDir, stdin, stdout, parseStateTimeout(st.StageTimeout)),
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
		fmt.Fprintf(stderr, "orch: %v\n", err)
		return 1
	}

	if err := b.build(st.Task); err != nil {
		fmt.Fprintf(stderr, "orch: resume: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "orch: resumed run %s to completion; artifacts in %s\n", id, runDir)
	return 0
}
