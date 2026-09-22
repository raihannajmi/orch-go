package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// decodeJSON unmarshals stdout and fails the test if it is not exactly one valid
// JSON document, which is the core --json contract.
func decodeJSON(t *testing.T, out *bytes.Buffer, v any) {
	t.Helper()
	trimmed := strings.TrimSpace(out.String())
	if trimmed == "" {
		t.Fatal("stdout is empty, want a JSON document")
	}
	if !strings.HasPrefix(trimmed, "{") {
		t.Fatalf("stdout does not start with '{':\n%s", trimmed)
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	if err := dec.Decode(v); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, trimmed)
	}
	if dec.More() {
		t.Fatalf("stdout holds more than one JSON document:\n%s", trimmed)
	}
}

func TestJSONRequested(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{[]string{"status", "--json"}, true},
		{[]string{"--json"}, true},
		{[]string{"build", "task"}, false},
		{[]string{"build", "--", "--json"}, false}, // after the terminator it is task text
	}
	for _, tt := range tests {
		if got := jsonRequested(tt.args); got != tt.want {
			t.Errorf("jsonRequested(%q) = %v, want %v", tt.args, got, tt.want)
		}
	}
}

func TestListJSON(t *testing.T) {
	var out, errb bytes.Buffer
	if code := orchMain([]string{"list", "--json"}, nil, &out, &errb); code != 0 {
		t.Fatalf("list --json = %d, want 0 (stderr: %s)", code, errb.String())
	}
	var resp listResponse
	decodeJSON(t, &out, &resp)
	if resp.Command != "list" || !resp.OK || resp.Error != nil {
		t.Errorf("resp = %+v, want ok list", resp.jsonHeader)
	}
	if len(resp.Agents) != len(agents) {
		t.Errorf("agents = %d, want %d", len(resp.Agents), len(agents))
	}
	for _, a := range resp.Agents {
		if a.Name == "" || a.Summary == "" {
			t.Errorf("agent entry incomplete: %+v", a)
		}
	}
}

func TestStatusJSONEmpty(t *testing.T) {
	t.Chdir(t.TempDir())
	var out, errb bytes.Buffer
	if code := orchMain([]string{"status", "--json"}, nil, &out, &errb); code != 0 {
		t.Fatalf("status --json = %d (stderr: %s)", code, errb.String())
	}
	var resp statusResponse
	decodeJSON(t, &out, &resp)
	if resp.Command != "status" || !resp.OK {
		t.Errorf("resp = %+v", resp.jsonHeader)
	}
	if resp.Runs == nil || len(resp.Runs) != 0 {
		t.Errorf("runs = %v, want an empty array", resp.Runs)
	}
}

func TestStatusJSONPopulated(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	runDir := writeState(t, dir, "20260101-120000", runState{
		Version: runStateVersion, ID: "20260101-120000", Status: runStateFailed,
		Failure: failureAgent, ExitCode: 7, VerifyResult: verifyFailed,
		Stages: []stageState{{Name: "1-plan", Agent: "agy", Artifact: "1-plan.md", Status: stageCompleted}},
	})
	writeArtifact(t, runDir, "4-review.md", "no\n\nVERDICT: REJECTED\n")

	var out, errb bytes.Buffer
	if code := orchMain([]string{"status", "--json"}, nil, &out, &errb); code != 0 {
		t.Fatalf("status --json = %d (stderr: %s)", code, errb.String())
	}
	var resp statusResponse
	decodeJSON(t, &out, &resp)
	if len(resp.Runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(resp.Runs))
	}
	r := resp.Runs[0]
	if r.ID != "20260101-120000" || r.Verdict != "REJECTED" {
		t.Errorf("run = %+v", r)
	}
	if r.Status != runStateFailed || r.Failure != failureAgent || r.ExitCode != 7 || r.VerifyResult != verifyFailed {
		t.Errorf("run state not surfaced: %+v", r)
	}
}

func TestStatusJSONRejectsUnknownArg(t *testing.T) {
	t.Chdir(t.TempDir())
	var out, errb bytes.Buffer
	if code := orchMain([]string{"status", "--json", "bogus"}, nil, &out, &errb); code != 2 {
		t.Fatalf("status --json bogus = %d, want 2", code)
	}
	var resp errorResponse
	decodeJSON(t, &out, &resp)
	if resp.OK || resp.Error == nil || resp.Error.Code != 2 {
		t.Errorf("resp = %+v, want a JSON error with code 2", resp)
	}
}

func TestLogsJSON(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	runDir := filepath.Join(dir, buildStateDir, "20260101-120000")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	writeArtifact(t, runDir, "1-plan.md", "plan")
	writeArtifact(t, runDir, "1-plan.log", "transcript")

	var out, errb bytes.Buffer
	if code := orchMain([]string{"logs", "--json", "20260101-120000"}, nil, &out, &errb); code != 0 {
		t.Fatalf("logs --json = %d (stderr: %s)", code, errb.String())
	}
	var resp logsResponse
	decodeJSON(t, &out, &resp)
	if resp.Command != "logs" || !resp.OK || resp.RunID != "20260101-120000" {
		t.Errorf("resp = %+v", resp)
	}
	if len(resp.Artifacts) != 2 {
		t.Errorf("artifacts = %+v, want 2", resp.Artifacts)
	}
}

func TestLogsJSONUnknownRun(t *testing.T) {
	t.Chdir(t.TempDir())
	var out, errb bytes.Buffer
	if code := orchMain([]string{"logs", "--json", "20250101-000000"}, nil, &out, &errb); code != 2 {
		t.Fatalf("logs --json unknown = %d, want 2", code)
	}
	var resp errorResponse
	decodeJSON(t, &out, &resp)
	if resp.Error == nil || resp.Error.Code != 2 {
		t.Errorf("resp = %+v, want a JSON error", resp)
	}
}

func TestBuildJSONUsageError(t *testing.T) {
	var out, errb bytes.Buffer
	// No task, with --json: the usage error must still be one JSON document.
	if code := orchMain([]string{"build", "--json"}, nil, &out, &errb); code != 2 {
		t.Fatalf("build --json (no task) = %d, want 2", code)
	}
	var resp errorResponse
	decodeJSON(t, &out, &resp)
	if resp.OK || resp.Error == nil || resp.Error.Code != 2 {
		t.Errorf("resp = %+v, want a JSON error with code 2", resp)
	}
}

func TestBuildJSONLockRefused(t *testing.T) {
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
	if code := orchMain([]string{"build", "-C", repo, "--json", "task"}, nil, &out, &errb); code != 2 {
		t.Fatalf("build --json (locked) = %d, want 2 (stderr: %s)", code, errb.String())
	}
	var resp errorResponse
	decodeJSON(t, &out, &resp)
	if resp.Error == nil || resp.Error.Code != 2 || !strings.Contains(resp.Error.Message, "already running") {
		t.Errorf("resp = %+v, want a lock-refusal JSON error", resp)
	}
}

func TestBuildJSONSuccess(t *testing.T) {
	dir := t.TempDir()
	standInPATH(t, installStandInAgents(t, ""))

	var out, errb bytes.Buffer
	if code := orchMain([]string{"build", "-C", dir, "--verify", "true", "--json", "do it"}, nil, &out, &errb); code != 0 {
		t.Fatalf("build --json = %d, want 0 (stderr: %s)", code, errb.String())
	}
	var resp runResponse
	decodeJSON(t, &out, &resp)
	if !resp.OK || resp.Error != nil || resp.Run == nil {
		t.Fatalf("resp = %+v, want an ok run", resp)
	}
	if resp.Run.Status != runStateCompleted || resp.Run.VerifyResult != verifyPassed {
		t.Errorf("run = %+v, want completed with verification passed", resp.Run)
	}
	if len(resp.Run.Stages) == 0 {
		t.Error("run.stages is empty")
	}
}

func TestBuildJSONAgentFailure(t *testing.T) {
	dir := t.TempDir()
	standInPATH(t, installStandInAgents(t, "#!/bin/sh\nexit 7\n"))

	var out, errb bytes.Buffer
	if code := orchMain([]string{"build", "-C", dir, "--verify", "true", "--json", "do it"}, nil, &out, &errb); code != 1 {
		t.Fatalf("build --json (agent fails) = %d, want 1 (stderr: %s)", code, errb.String())
	}
	var resp runResponse
	decodeJSON(t, &out, &resp)
	if resp.OK || resp.Error == nil || resp.Error.Code != 1 {
		t.Fatalf("resp = %+v, want a failure document", resp)
	}
	if resp.Run == nil || resp.Run.Status != runStateFailed {
		t.Fatalf("run = %+v, want a failed run", resp.Run)
	}
	if resp.Run.Failure != failureAgent || resp.Run.ExitCode != 7 {
		t.Errorf("run failure = %q exitCode = %d, want agent/7", resp.Run.Failure, resp.Run.ExitCode)
	}
}

func TestBuildJSONVerificationFailure(t *testing.T) {
	dir := t.TempDir()
	standInPATH(t, installStandInAgents(t, ""))

	var out, errb bytes.Buffer
	if code := orchMain([]string{"build", "-C", dir, "--verify", "false", "--json", "do it"}, nil, &out, &errb); code != 1 {
		t.Fatalf("build --json (verify fails) = %d, want 1 (stderr: %s)", code, errb.String())
	}
	var resp runResponse
	decodeJSON(t, &out, &resp)
	if resp.Run == nil || resp.Run.Status != runStateFailed {
		t.Fatalf("run = %+v, want a failed run", resp.Run)
	}
	if resp.Run.Failure != failureVerify || resp.Run.VerifyResult != verifyFailed {
		t.Errorf("run = %+v, want failure=verify verifyResult=failed", resp.Run)
	}
}

func TestResumeJSONAlreadyComplete(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeState(t, dir, "20260101-120000", runState{
		Version: runStateVersion, ID: "20260101-120000",
		Status: runStateCompleted, Stages: []stageState{},
	})

	var out, errb bytes.Buffer
	if code := orchMain([]string{"resume", "20260101-120000", "--json"}, nil, &out, &errb); code != 0 {
		t.Fatalf("resume --json = %d, want 0 (stderr: %s)", code, errb.String())
	}
	var resp runResponse
	decodeJSON(t, &out, &resp)
	if !resp.OK || resp.Run == nil || resp.Run.Status != runStateCompleted {
		t.Errorf("resp = %+v, want an ok completed run", resp)
	}
}

// TestJSONNeverLeaksTaskText guards against leaking user content into
// machine-readable output.
func TestJSONNeverLeaksTaskText(t *testing.T) {
	dir := t.TempDir()
	standInPATH(t, installStandInAgents(t, ""))

	const secret = "SUPER-SECRET-TASK-TEXT"
	var out, errb bytes.Buffer
	if code := orchMain([]string{"build", "-C", dir, "--verify", "true", "--json", secret}, nil, &out, &errb); code != 0 {
		t.Fatalf("build --json = %d (stderr: %s)", code, errb.String())
	}
	if strings.Contains(out.String(), secret) {
		t.Errorf("the task text leaked into JSON output:\n%s", out.String())
	}
}
