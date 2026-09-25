package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// initGitRepo creates a committed git repository in a temp directory, skipping
// the test when git is unavailable.
func initGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "orch-test@example.com")
	runGit(t, dir, "config", "user.name", "orch test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// writeState creates .orch/<id>/run.json under base and returns the run dir.
func writeState(t *testing.T, base, id string, st runState) string {
	t.Helper()
	dir := filepath.Join(base, buildStateDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	if err := writeRunState(dir, st); err != nil {
		t.Fatalf("writeRunState: %v", err)
	}
	return dir
}

// TestResumeReplaysCompletedStages is the core resume behaviour: a completed
// stage's artifact is read back from disk and reused, so the workflow re-enters
// at the first incomplete stage without re-launching an earlier agent.
func TestResumeReplaysCompletedStages(t *testing.T) {
	runDir := t.TempDir()
	writeArtifact(t, runDir, "1-plan.md", "the plan\n"+stageMarker+"\n")

	st := runState{
		Status: runStateRunning,
		Stages: []stageState{{Name: "1-plan", Agent: "agy", Artifact: "1-plan.md", Status: stageCompleted}},
	}

	var calls []string
	prompts := map[string]string{}
	b := &builder{
		dir:        t.TempDir(),
		runDir:     runDir,
		stdout:     io.Discard,
		stderr:     io.Discard,
		state:      &st,
		resumeFrom: 1,
	}
	b.stage = func(agentName, name, prompt string) (string, error) {
		calls = append(calls, name)
		prompts[name] = prompt
		if strings.HasSuffix(name, "-review") {
			return "looks correct\n\nVERDICT: APPROVED", nil
		}
		return name + " body", nil
	}
	b.verify = func() error { return nil }

	if err := b.build("resume the task"); err != nil {
		t.Fatalf("build: %v", err)
	}

	want := []string{"2-plan-review", "3-implement", "4-review"}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Errorf("stages = %v, want %v (1-plan must be replayed, not re-run)", calls, want)
	}
	if !strings.Contains(prompts["2-plan-review"], "the plan") {
		t.Errorf("plan-review prompt did not inherit the replayed plan:\n%s", prompts["2-plan-review"])
	}
	if st.Status != runStateCompleted {
		t.Errorf("status = %q, want completed", st.Status)
	}
	if len(st.Stages) != 4 {
		t.Errorf("stages = %d, want 4 (1 replayed + 3 run)", len(st.Stages))
	}
}

// TestResumeRerunsStageWithoutMarker checks that a stage whose artifact never
// got the completion marker is re-run rather than trusted from run.json.
func TestResumeRerunsStageWithoutMarker(t *testing.T) {
	runDir := t.TempDir()
	writeArtifact(t, runDir, "1-plan.md", "the plan\n"+stageMarker+"\n")
	writeArtifact(t, runDir, "2-plan-review.md", "half written, no marker")

	st := runState{
		Status: runStateRunning,
		Stages: []stageState{
			{Name: "1-plan", Agent: "agy", Artifact: "1-plan.md", Status: stageCompleted},
			{Name: "2-plan-review", Agent: "command-code", Artifact: "2-plan-review.md", Status: stageRunning},
		},
	}
	resumeFrom := firstIncompleteStage(runDir, st)
	if resumeFrom != 1 {
		t.Fatalf("firstIncompleteStage = %d, want 1", resumeFrom)
	}

	st.Stages = st.Stages[:resumeFrom]
	var calls []string
	b := &builder{
		dir:        t.TempDir(),
		runDir:     runDir,
		stdout:     io.Discard,
		stderr:     io.Discard,
		state:      &st,
		resumeFrom: resumeFrom,
	}
	b.stage = func(agentName, name, prompt string) (string, error) {
		calls = append(calls, name)
		if strings.HasSuffix(name, "-review") {
			return "ok\n\nVERDICT: APPROVED", nil
		}
		return name + " body", nil
	}
	b.verify = func() error { return nil }

	if err := b.build("resume the task"); err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(calls) == 0 || calls[0] != "2-plan-review" {
		t.Fatalf("stages = %v, want 2-plan-review to be re-run first", calls)
	}
	if strings.Contains(strings.Join(calls, ","), "1-plan") {
		t.Errorf("stages = %v, want 1-plan replayed, not re-run", calls)
	}
}

// TestCmdResumeRefusesSymlinkedState covers the documented guarantee that a
// symlinked .orch is refused. `build` enforces it; `resume` is also a write path
// (it rewrites run.json and writes stage transcripts), so it must not let an
// untrusted repository redirect those writes through a symlink.
func TestCmdResumeRefusesSymlinkedState(t *testing.T) {
	mkRun := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir run: %v", err)
		}
		// A run that would otherwise look resumable, so only the symlink stops it.
		if err := writeRunState(dir, runState{
			Version: runStateVersion, ID: "20260101-120000", Task: "t",
			Status: runStateRunning,
			Stages: []stageState{{Name: "1-plan", Agent: "agy", Artifact: "1-plan.md", Status: stageRunning}},
		}); err != nil {
			t.Fatalf("writeRunState: %v", err)
		}
	}

	t.Run("state directory is a symlink", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		target := t.TempDir()
		mkRun(t, filepath.Join(target, "20260101-120000"))
		link := filepath.Join(dir, buildStateDir)
		if err := os.Symlink(target, link); err != nil {
			if runtime.GOOS == "windows" {
				t.Skip("symlinks are not generally available on Windows")
			}
			t.Skipf("symlink unavailable: %v", err)
		}

		var out, errb bytes.Buffer
		if got := cmdResume([]string{"20260101-120000"}, nil, &out, &errb); got != 1 {
			t.Fatalf("cmdResume = %d, want 1 (stderr: %s)", got, errb.String())
		}
		if !strings.Contains(errb.String(), "symlink") {
			t.Errorf("stderr = %q, want a symlink refusal", errb.String())
		}
		// Nothing may have been written through the link.
		if _, err := os.Stat(filepath.Join(target, workflowLockFile)); err == nil {
			t.Error("resume took the workflow lock through the symlinked .orch")
		}
		if _, err := os.Stat(filepath.Join(target, "20260101-120000", runLockFile)); err == nil {
			t.Error("resume took the run lock through the symlinked .orch")
		}
	})

	t.Run("run directory is a symlink", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		stateDir := filepath.Join(dir, buildStateDir)
		if err := os.MkdirAll(stateDir, 0o700); err != nil {
			t.Fatalf("mkdir state dir: %v", err)
		}
		real := filepath.Join(dir, "elsewhere")
		mkRun(t, real)
		if err := os.Symlink(real, filepath.Join(stateDir, "20260101-120000")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}

		var out, errb bytes.Buffer
		if got := cmdResume([]string{"20260101-120000"}, nil, &out, &errb); got != 1 {
			t.Fatalf("cmdResume = %d, want 1 (stderr: %s)", got, errb.String())
		}
		if !strings.Contains(errb.String(), "symlink") {
			t.Errorf("stderr = %q, want a symlink refusal", errb.String())
		}
		if _, err := os.Stat(filepath.Join(real, runLockFile)); err == nil {
			t.Error("resume took the run lock through the symlinked run directory")
		}
	})
}

// TestCmdResumeUnknownRunCreatesNoState pins that resuming an unknown id is a
// pure read: it must not create a .orch directory as a side effect.
func TestCmdResumeUnknownRunCreatesNoState(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	var out, errb bytes.Buffer
	if got := cmdResume([]string{"20250101-000000"}, nil, &out, &errb); got != 2 {
		t.Fatalf("cmdResume = %d, want 2 (stderr: %s)", got, errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, buildStateDir)); !os.IsNotExist(err) {
		t.Errorf("resume created %s for an unknown run (stat err = %v)", buildStateDir, err)
	}
}

// TestCmdResumeUsageErrors covers the id and flag validation of resume.
func TestCmdResumeUsageErrors(t *testing.T) {
	t.Chdir(t.TempDir())
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"no id", nil, 2},
		{"unknown id", []string{"20250101-000000"}, 2},
		{"traversal", []string{"../escape"}, 2},
		{"separator", []string{"nested/run"}, 2},
		{"unknown flag", []string{"--nope"}, 2},
		{"help", []string{"-h"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			if got := cmdResume(tt.args, nil, &out, &errb); got != tt.want {
				t.Errorf("cmdResume(%q) = %d, want %d (stderr: %s)", tt.args, got, tt.want, errb.String())
			}
		})
	}
}

// TestCmdResumeLegacyRun covers backward compatibility: a run written before
// run.json existed stays readable but is refused by resume.
func TestCmdResumeLegacyRun(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	runDir := filepath.Join(dir, buildStateDir, "20260101-120000")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	writeArtifact(t, runDir, "1-plan.md", "plan")

	var out, errb bytes.Buffer
	if got := cmdResume([]string{"20260101-120000"}, nil, &out, &errb); got != 2 {
		t.Fatalf("cmdResume = %d, want 2 (stderr: %s)", got, errb.String())
	}
	if !strings.Contains(errb.String(), "predates resume") {
		t.Errorf("stderr = %q, want a legacy-run message", errb.String())
	}
}

// TestCmdResumeCompletedIsNoop covers requirement 9: a completed run is a no-op.
func TestCmdResumeCompletedIsNoop(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeState(t, dir, "20260101-120000", runState{
		Version: runStateVersion, ID: "20260101-120000",
		Status: runStateCompleted, Stages: []stageState{},
	})

	var out, errb bytes.Buffer
	if got := cmdResume([]string{"20260101-120000"}, nil, &out, &errb); got != 0 {
		t.Fatalf("cmdResume = %d, want 0 (stderr: %s)", got, errb.String())
	}
	if !strings.Contains(out.String(), "already complete") {
		t.Errorf("stdout = %q, want an already-complete message", out.String())
	}
}

// TestCmdResumeConcurrentIsRefused covers concurrent resume protection: while a
// run's lock is held, resume must refuse rather than share the directory.
func TestCmdResumeConcurrentIsRefused(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	runDir := writeState(t, dir, "20260101-120000", runState{
		Version: runStateVersion, ID: "20260101-120000",
		Status: runStateRunning, Stages: []stageState{},
	})

	lock, err := lockRun(runDir)
	if err != nil {
		t.Fatalf("lockRun: %v", err)
	}
	defer func() { _ = lock.Close() }()

	var out, errb bytes.Buffer
	if got := cmdResume([]string{"20260101-120000"}, nil, &out, &errb); got != 2 {
		t.Fatalf("cmdResume = %d, want 2 (stderr: %s)", got, errb.String())
	}
	if !strings.Contains(errb.String(), "already in progress") {
		t.Errorf("stderr = %q, want a lock message", errb.String())
	}
}

// TestCmdResumeMarksInterruptedThenRefuses covers a run whose state says
// "running" but whose lock is free: it was interrupted. The interruption is
// recorded, and the resume is refused because the repository cannot be proven
// safe (no base commit here).
func TestCmdResumeMarksInterruptedThenRefuses(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	runDir := writeState(t, dir, "20260101-120000", runState{
		Version: runStateVersion, ID: "20260101-120000", Task: "t", RepoDir: dir,
		Status: runStateRunning,
		Stages: []stageState{{Name: "1-plan", Agent: "agy", Artifact: "1-plan.md", Status: stageRunning}},
	})

	var out, errb bytes.Buffer
	if got := cmdResume([]string{"20260101-120000"}, nil, &out, &errb); got != 1 {
		t.Fatalf("cmdResume = %d, want 1 (stderr: %s)", got, errb.String())
	}
	if !strings.Contains(errb.String(), "cannot resume") {
		t.Errorf("stderr = %q, want a refusal", errb.String())
	}
	st, err := readRunState(runDir)
	if err != nil {
		t.Fatalf("readRunState: %v", err)
	}
	if st.Status != runStateInterrupted {
		t.Errorf("status = %q, want interrupted to be recorded", st.Status)
	}
}

// TestCmdResumeRefusesDirtyTree covers requirement 13 end to end: a repository
// with an unexpected change and no mutating stage completed is refused.
func TestCmdResumeRefusesDirtyTree(t *testing.T) {
	repo := initGitRepo(t)
	head, err := gitHead(repo)
	if err != nil {
		t.Fatalf("gitHead: %v", err)
	}
	t.Chdir(repo)

	dir := filepath.Join(repo, buildStateDir, "20260101-120000")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	if err := writeRunState(dir, runState{
		Version: runStateVersion, ID: "20260101-120000", Task: "t", RepoDir: repo,
		Status: runStateRunning, BaseCommit: head,
		Stages: []stageState{{Name: "1-plan", Agent: "agy", Artifact: "1-plan.md", Status: stageRunning}},
	}); err != nil {
		t.Fatalf("writeRunState: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	var out, errb bytes.Buffer
	if got := cmdResume([]string{"20260101-120000"}, nil, &out, &errb); got != 1 {
		t.Fatalf("cmdResume = %d, want 1 (stderr: %s)", got, errb.String())
	}
	if !strings.Contains(errb.String(), "cannot resume") {
		t.Errorf("stderr = %q, want a refusal naming the unsafe repository", errb.String())
	}
}

// TestCmdResumeRefusesMovedHead covers a repository whose HEAD moved after the
// run started: resume refuses instead of disturbing the user's commits.
func TestCmdResumeRefusesMovedHead(t *testing.T) {
	repo := initGitRepo(t)
	head, err := gitHead(repo)
	if err != nil {
		t.Fatalf("gitHead: %v", err)
	}
	t.Chdir(repo)

	dir := filepath.Join(repo, buildStateDir, "20260101-120000")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	if err := writeRunState(dir, runState{
		Version: runStateVersion, ID: "20260101-120000", Task: "t", RepoDir: repo,
		Status: runStateRunning, BaseCommit: head,
		Stages: []stageState{{Name: "1-plan", Agent: "agy", Artifact: "1-plan.md", Status: stageRunning}},
	}); err != nil {
		t.Fatalf("writeRunState: %v", err)
	}
	// A new commit moves HEAD away from the recorded base.
	if err := os.WriteFile(filepath.Join(repo, "second.txt"), []byte("y"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-q", "-m", "move head")

	var out, errb bytes.Buffer
	if got := cmdResume([]string{"20260101-120000"}, nil, &out, &errb); got != 1 {
		t.Fatalf("cmdResume = %d, want 1 (stderr: %s)", got, errb.String())
	}
	if !strings.Contains(errb.String(), "HEAD moved") {
		t.Errorf("stderr = %q, want a moved-HEAD refusal", errb.String())
	}
}

// TestResumeRefusesFailedWithoutIncompleteStage covers a run that failed for a
// workflow reason (reviews never approved) rather than an interruption: there is
// no incomplete stage, so resume refuses instead of silently doing nothing.
func TestResumeRefusesFailedWithoutIncompleteStage(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeState(t, dir, "20260101-120000", runState{
		Version: runStateVersion, ID: "20260101-120000", Task: "t", RepoDir: dir,
		Status: runStateFailed, Error: "implementation was not approved after 3 review cycles",
		Stages: []stageState{{Name: "1-plan", Agent: "agy", Artifact: "1-plan.md", Status: stageCompleted}},
	})
	writeArtifact(t, filepath.Join(dir, buildStateDir, "20260101-120000"), "1-plan.md", "plan\n"+stageMarker+"\n")

	var out, errb bytes.Buffer
	if got := cmdResume([]string{"20260101-120000"}, nil, &out, &errb); got != 1 {
		t.Fatalf("cmdResume = %d, want 1 (stderr: %s)", got, errb.String())
	}
	if !strings.Contains(errb.String(), "no incomplete stage") {
		t.Errorf("stderr = %q, want a no-incomplete-stage message", errb.String())
	}
}

// TestResumeInterruptedImplementStage covers resuming when the interrupted stage
// is the middle, mutating implement stage: the completed prefix is replayed, the
// implement stage is re-run, and the progress cursor never counts the
// interrupted stage as done before it actually completes.
func TestResumeInterruptedImplementStage(t *testing.T) {
	runDir := t.TempDir()
	writeArtifact(t, runDir, "1-plan.md", "the plan\n"+stageMarker+"\n")
	writeArtifact(t, runDir, "2-plan-review.md", "looks fine\n"+stageMarker+"\n")
	// 3-implement was interrupted: a partial artifact with no completion marker.
	writeArtifact(t, runDir, "3-implement.md", "half-written changes")

	st := runState{Status: runStateRunning, LastCompleted: 2, Stages: []stageState{
		{Name: "1-plan", Agent: "agy", Artifact: "1-plan.md", Status: stageCompleted},
		{Name: "2-plan-review", Agent: "command-code", Artifact: "2-plan-review.md", Status: stageCompleted},
		{Name: "3-implement", Agent: "agy", Artifact: "3-implement.md", Status: stageRunning},
	}}
	resumeFrom := firstIncompleteStage(runDir, st)
	if resumeFrom != 2 {
		t.Fatalf("firstIncompleteStage = %d, want 2 (the interrupted implement stage)", resumeFrom)
	}
	// The recorded tail is discarded, as cmdResume does.
	st.Stages = st.Stages[:resumeFrom]
	if st.LastCompleted != 2 {
		t.Fatalf("lastCompleted = %d, want 2 after truncating the incomplete tail", st.LastCompleted)
	}

	var calls []string
	b := &builder{
		dir: t.TempDir(), runDir: runDir, stdout: io.Discard, stderr: io.Discard,
		state: &st, resumeFrom: resumeFrom,
	}
	b.stage = func(agentName, name, prompt string) (string, error) {
		calls = append(calls, name)
		if strings.HasSuffix(name, "-review") {
			return "approved\n\nVERDICT: APPROVED", nil
		}
		return name + " body", nil
	}
	b.verify = func() error { return nil }

	if err := b.build("resume the task"); err != nil {
		t.Fatalf("build: %v", err)
	}
	want := []string{"3-implement", "4-review"}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Errorf("stages = %v, want %v (prefix replayed, implement re-run)", calls, want)
	}
	if st.LastCompleted != len(st.Stages) {
		t.Errorf("lastCompleted = %d, want %d", st.LastCompleted, len(st.Stages))
	}
	for _, s := range st.Stages {
		if s.Status != stageCompleted {
			t.Errorf("stage %s status = %q, want completed", s.Name, s.Status)
		}
	}
}
