package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// writeArtifact writes one stage artifact into a run directory.
func writeArtifact(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// TestCreateRunDirCollisionSafe covers two runs started in the same second: the
// old MkdirAll let them silently share a directory; createRunDir must not.
func TestCreateRunDirCollisionSafe(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), buildStateDir)

	id1, dir1, err := createRunDir(stateDir)
	if err != nil {
		t.Fatalf("first createRunDir: %v", err)
	}
	id2, dir2, err := createRunDir(stateDir)
	if err != nil {
		t.Fatalf("second createRunDir: %v", err)
	}
	if id1 == id2 || dir1 == dir2 {
		t.Fatalf("same-second runs collided: %q/%q and %q/%q", id1, dir1, id2, dir2)
	}
	for _, dir := range []string{dir1, dir2} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("run dir %s was not created: %v", dir, err)
		}
	}
}

func TestRunStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := runState{
		Version:       runStateVersion,
		ID:            "20260101-120000",
		Task:          "do a thing",
		RepoDir:       "/repo",
		CreatedAt:     "2026-01-01T12:00:00Z",
		StageTimeout:  "30m0s",
		Knowledge:     true,
		Status:        runStateRunning,
		LastCompleted: 2,
		BaseCommit:    "abc123",
		Stages: []stageState{
			{Name: "1-plan", Agent: "agy", Artifact: "1-plan.md", Status: stageCompleted},
			{Name: "2-plan-review", Agent: "command-code", Artifact: "2-plan-review.md", Status: stageRunning},
		},
	}
	if err := writeRunState(dir, want); err != nil {
		t.Fatalf("writeRunState: %v", err)
	}
	got, err := readRunState(dir)
	if err != nil {
		t.Fatalf("readRunState: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

// TestWriteRunStateIsAtomic checks that a state write replaces the previous one
// and leaves no temporary file behind.
func TestWriteRunStateIsAtomic(t *testing.T) {
	dir := t.TempDir()
	if err := writeRunState(dir, runState{Status: runStateRunning}); err != nil {
		t.Fatalf("first writeRunState: %v", err)
	}
	if err := writeRunState(dir, runState{Status: runStateCompleted}); err != nil {
		t.Fatalf("second writeRunState: %v", err)
	}
	got, err := readRunState(dir)
	if err != nil {
		t.Fatalf("readRunState: %v", err)
	}
	if got.Status != runStateCompleted {
		t.Errorf("status = %q, want completed", got.Status)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temporary state file left behind: %s", e.Name())
		}
	}
}

// TestLockRunExclusive covers concurrent protection: a second lock holder is
// refused until the first releases, and the lock is reacquirable afterwards.
func TestLockRunExclusive(t *testing.T) {
	dir := t.TempDir()

	first, err := lockRun(dir)
	if err != nil {
		t.Fatalf("first lockRun: %v", err)
	}
	if _, err := lockRun(dir); !errors.Is(err, errRunLocked) {
		t.Fatalf("second lockRun = %v, want errRunLocked", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("release: %v", err)
	}

	second, err := lockRun(dir)
	if err != nil {
		t.Fatalf("lockRun after release: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("release second: %v", err)
	}
}

// TestFirstIncompleteStage covers the artifact-validation rule: completion is
// decided by the marker protocol, not by run.json's status alone.
func TestFirstIncompleteStage(t *testing.T) {
	dir := t.TempDir()
	writeArtifact(t, dir, "1-plan.md", "plan\n"+stageMarker+"\n")
	writeArtifact(t, dir, "2-plan-review.md", "partial without the marker")

	stages := func(names ...string) []stageState {
		out := make([]stageState, 0, len(names))
		for _, n := range names {
			out = append(out, stageState{Name: n, Artifact: n + ".md"})
		}
		return out
	}

	if got := firstIncompleteStage(dir, runState{Stages: stages("1-plan", "2-plan-review", "3-implement")}); got != 1 {
		t.Errorf("marker-less stage: firstIncompleteStage = %d, want 1", got)
	}
	if got := firstIncompleteStage(dir, runState{Stages: stages("1-plan", "9-missing")}); got != 1 {
		t.Errorf("missing artifact: firstIncompleteStage = %d, want 1", got)
	}
	if got := firstIncompleteStage(dir, runState{Stages: stages("1-plan")}); got != 1 {
		t.Errorf("all complete: firstIncompleteStage = %d, want 1 (= len)", got)
	}
	if got := firstIncompleteStage(dir, runState{}); got != 0 {
		t.Errorf("no stages: firstIncompleteStage = %d, want 0", got)
	}
	if got := firstIncompleteStage(dir, runState{Stages: stages("2-plan-review")}); got != 0 {
		t.Errorf("lost marker: firstIncompleteStage = %d, want 0", got)
	}
}

func TestRepoMutatedByRun(t *testing.T) {
	if !mutatingStage("3-implement") || !mutatingStage("5-fix") {
		t.Error("implement/fix stages should count as mutating")
	}
	if mutatingStage("1-plan") || mutatingStage("4-review") {
		t.Error("plan/review stages should not count as mutating")
	}
	if !repoMutatedByRun(runState{Stages: []stageState{{Name: "3-implement"}}}) {
		t.Error("a recorded implement stage should mark the run as mutating")
	}
	if repoMutatedByRun(runState{Stages: []stageState{{Name: "1-plan"}}}) {
		t.Error("a plan-only run should not be mutating")
	}
}

func TestFormatParseStageTimeout(t *testing.T) {
	if got := formatStageTimeout(0); got != "disabled" {
		t.Errorf("formatStageTimeout(0) = %q, want disabled", got)
	}
	if got := formatStageTimeout(30 * time.Minute); got != "30m0s" {
		t.Errorf("formatStageTimeout(30m) = %q, want 30m0s", got)
	}
	if got := parseStateTimeout("disabled"); got != 0 {
		t.Errorf("parseStateTimeout(disabled) = %s, want 0", got)
	}
	if got := parseStateTimeout("45s"); got != 45*time.Second {
		t.Errorf("parseStateTimeout(45s) = %s, want 45s", got)
	}
	if got := parseStateTimeout("garbage"); got != defaultStageTimeout {
		t.Errorf("parseStateTimeout(garbage) = %s, want the default %s", got, defaultStageTimeout)
	}
}

// TestCheckResumeSafe covers the repository safety gate: a moved HEAD, a missing
// base commit and an unexpected dirty tree all refuse, while a dirty tree left by
// a mutating stage only warns.
func TestCheckResumeSafe(t *testing.T) {
	repo := initGitRepo(t)
	base, err := gitHead(repo)
	if err != nil {
		t.Fatalf("gitHead: %v", err)
	}

	if warn, err := checkResumeSafe(repo, base, false); err != nil || warn != "" {
		t.Errorf("clean tree: warn=%q err=%v, want nil,nil", warn, err)
	}

	// orch's own state directory must never be mistaken for a user change.
	if err := os.MkdirAll(filepath.Join(repo, buildStateDir, "20260101-120000"), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if _, err := checkResumeSafe(repo, base, false); err != nil {
		t.Errorf("state dir counted as a change: %v", err)
	}

	// An unrelated dirty file with no mutating stage is unsafe to resume.
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}
	if _, err := checkResumeSafe(repo, base, false); err == nil {
		t.Error("dirty tree with no mutating stage = nil error, want a refusal")
	}
	// The same dirty tree is only a warning once a mutating stage is recorded.
	warn, err := checkResumeSafe(repo, base, true)
	if err != nil {
		t.Errorf("dirty tree with a mutating stage = %v, want a warning", err)
	}
	if warn == "" {
		t.Error("expected a warning about changes that are not rolled back")
	}

	if _, err := checkResumeSafe(repo, strings.Repeat("0", 40), false); err == nil {
		t.Error("moved HEAD = nil error, want a refusal")
	}
	if _, err := checkResumeSafe(repo, "", false); err == nil {
		t.Error("empty base commit = nil error, want a refusal")
	}
}

// TestBuildPersistsState checks that a completed run leaves a full, consistent
// run.json behind.
func TestBuildPersistsState(t *testing.T) {
	b, _, _, _ := fakeBuilder(t, 1)
	b.state = &runState{Version: runStateVersion, Status: runStateRunning, Stages: []stageState{}}

	if err := b.build("persist the state"); err != nil {
		t.Fatalf("build: %v", err)
	}

	st, err := readRunState(b.runDir)
	if err != nil {
		t.Fatalf("readRunState: %v", err)
	}
	if st.Status != runStateCompleted {
		t.Errorf("status = %q, want completed", st.Status)
	}
	if st.VerifyResult != verifyPassed {
		t.Errorf("verifyResult = %q, want passed", st.VerifyResult)
	}
	if st.Task != "persist the state" {
		t.Errorf("task = %q, want %q", st.Task, "persist the state")
	}
	if len(st.Stages) != 4 {
		t.Fatalf("stages = %d, want 4", len(st.Stages))
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

// TestBuildRecordsFailedState checks that a run rejected after max cycles still
// leaves an explicit failed outcome, with verification never having run.
func TestBuildRecordsFailedState(t *testing.T) {
	b, _, _, _ := fakeBuilder(t, 99) // never approves
	b.state = &runState{Status: runStateRunning, Stages: []stageState{}}

	if err := b.build("never approved"); err == nil {
		t.Fatal("build succeeded, want failure after max cycles")
	}
	st, err := readRunState(b.runDir)
	if err != nil {
		t.Fatalf("readRunState: %v", err)
	}
	if st.Status != runStateFailed {
		t.Errorf("status = %q, want failed", st.Status)
	}
	if st.Error == "" {
		t.Error("failed run recorded no error")
	}
	if st.VerifyResult != "" {
		t.Errorf("verifyResult = %q, want empty (verification never ran)", st.VerifyResult)
	}
}

// TestBuildRecordsTimedOutState checks that a watchdog timeout is persisted as a
// distinct terminal state at both the run and the stage level.
func TestBuildRecordsTimedOutState(t *testing.T) {
	b, _, _, _ := fakeBuilder(t, 1)
	b.state = &runState{Status: runStateRunning, Stages: []stageState{}}

	inner := b.stage
	b.stage = func(agentName, name, prompt string) (string, error) {
		if name == "1-plan" {
			return "", errStageTimeout
		}
		return inner(agentName, name, prompt)
	}

	if err := b.build("timeout"); !errors.Is(err, errStageTimeout) {
		t.Fatalf("build error = %v, want errStageTimeout", err)
	}
	st, err := readRunState(b.runDir)
	if err != nil {
		t.Fatalf("readRunState: %v", err)
	}
	if st.Status != runStateTimedOut {
		t.Errorf("status = %q, want timed_out", st.Status)
	}
	if len(st.Stages) != 1 || st.Stages[0].Status != stageTimedOut {
		t.Errorf("stages = %+v, want the plan stage recorded as timed_out", st.Stages)
	}
}

// newStateBuilder builds a builder that persists state into a temp run dir but
// launches no agents, so the state transitions can be driven directly.
func newStateBuilder(t *testing.T) *builder {
	t.Helper()
	return &builder{
		runDir: t.TempDir(),
		stdout: io.Discard,
		stderr: io.Discard,
		state:  &runState{Stages: []stageState{}},
	}
}

// TestLastCompletedDoesNotCountIncompleteTail is the regression for the state
// bug: replaying a completed stage must not report the number of stage records
// as progress while a later stage is still running. The old code set
// LastCompleted = len(Stages), which is 2 here.
func TestLastCompletedDoesNotCountIncompleteTail(t *testing.T) {
	b := &builder{
		runDir: t.TempDir(), stdout: io.Discard, stderr: io.Discard,
		state: &runState{Stages: []stageState{
			{Name: "1-plan", Agent: "agy", Artifact: "1-plan.md", Status: stageCompleted},
			{Name: "2-plan-review", Agent: "command-code", Artifact: "2-plan-review.md", Status: stageRunning},
		}},
	}
	b.completeStage("1-plan")
	if got := b.state.LastCompleted; got != 1 {
		t.Errorf("lastCompleted = %d, want 1 (stage 2 is still running, not done)", got)
	}
}

// TestLastCompletedTracksContiguousPrefix follows a run through begin/complete/
// fail transitions, including a failed middle stage that must not advance it.
func TestLastCompletedTracksContiguousPrefix(t *testing.T) {
	b := newStateBuilder(t)

	b.beginStage("1-plan", "agy")
	if got := b.state.LastCompleted; got != 0 {
		t.Errorf("running stage counted as progress: lastCompleted = %d, want 0", got)
	}
	b.completeStage("1-plan")
	if got := b.state.LastCompleted; got != 1 {
		t.Errorf("lastCompleted = %d, want 1", got)
	}

	b.beginStage("2-plan-review", "command-code")
	if got := b.state.LastCompleted; got != 1 {
		t.Errorf("running stage advanced the cursor: lastCompleted = %d, want 1", got)
	}
	b.completeStage("2-plan-review")

	// A failed middle stage must not advance the cursor.
	b.beginStage("3-implement", "agy")
	b.failStage("3-implement", errors.New("agent exited with code 1"))
	if got := b.state.LastCompleted; got != 2 {
		t.Errorf("failed stage advanced the cursor: lastCompleted = %d, want 2", got)
	}

	// Re-running it still does not advance until it actually completes.
	b.beginStage("3-implement", "agy")
	if got := b.state.LastCompleted; got != 2 {
		t.Errorf("re-running advanced the cursor: lastCompleted = %d, want 2", got)
	}
	b.completeStage("3-implement")
	if got := b.state.LastCompleted; got != 3 {
		t.Errorf("lastCompleted = %d, want 3", got)
	}
}

// TestBeginStageRewindsLastCompleted covers re-running a stage that had already
// completed (its artifact was lost): the cursor rewinds to just before it.
func TestBeginStageRewindsLastCompleted(t *testing.T) {
	b := &builder{
		runDir: t.TempDir(), stdout: io.Discard, stderr: io.Discard,
		state: &runState{LastCompleted: 3, Stages: []stageState{
			{Name: "1-plan", Status: stageCompleted},
			{Name: "2-plan-review", Status: stageCompleted},
			{Name: "3-implement", Status: stageCompleted},
		}},
	}
	b.beginStage("3-implement", "agy")
	if got := b.state.LastCompleted; got != 2 {
		t.Errorf("lastCompleted = %d, want 2 after re-running stage 3", got)
	}
}

// TestRunIDIsUTC pins the run id to the UTC wall clock: it is parsed as a UTC
// timestamp and must be within a couple of seconds of UTC now. On a host with a
// non-UTC timezone a local-time id would be off by the offset and fail.
func TestRunIDIsUTC(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), buildStateDir)
	id, _, err := createRunDir(stateDir)
	if err != nil {
		t.Fatalf("createRunDir: %v", err)
	}
	got, err := time.Parse("20060102-150405", id)
	if err != nil {
		t.Fatalf("run id %q is not a timestamp: %v", id, err)
	}
	if d := time.Since(got); d < -3*time.Second || d > 3*time.Second {
		t.Errorf("run id %q is %s from UTC now; want a UTC timestamp", id, d)
	}
}
