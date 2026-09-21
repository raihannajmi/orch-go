package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRun creates a .orch/<id> directory and fills it with the given files.
func writeRun(t *testing.T, stateDir, id string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(stateDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

func TestListRuns(t *testing.T) {
	state := t.TempDir()
	writeRun(t, state, "20260101-120000", map[string]string{
		"1-plan.md":   "plan",
		"1-plan.log":  "transcript",
		"4-review.md": "ok\n\nVERDICT: APPROVED\n",
		"verify.log":  "clean",
	})
	writeRun(t, state, "20260102-090000", map[string]string{
		"1-plan.md":   "plan",
		"4-review.md": "needs work\n\nVERDICT: REJECTED\n",
	})
	writeRun(t, state, "20260103-080000", map[string]string{
		"1-plan.md": "plan",
	})

	runs, err := listRuns(state)
	if err != nil {
		t.Fatalf("listRuns: %v", err)
	}
	want := []runInfo{
		{id: "20260103-080000", stages: 1, verdict: "-"},
		{id: "20260102-090000", stages: 2, verdict: "REJECTED"},
		{id: "20260101-120000", stages: 2, verdict: "APPROVED"},
	}
	if len(runs) != len(want) {
		t.Fatalf("got %d runs, want %d: %+v", len(runs), len(want), runs)
	}
	for i := range want {
		if runs[i] != want[i] {
			t.Errorf("runs[%d] = %+v, want %+v", i, runs[i], want[i])
		}
	}
}

// TestListRunsUsesLastReview covers a fix cycle: a rejection followed by an
// approval must report the later verdict, not the first one read.
func TestListRunsUsesLastReview(t *testing.T) {
	state := t.TempDir()
	writeRun(t, state, "20260101-120000", map[string]string{
		"4-review.md": "no\n\nVERDICT: REJECTED\n",
		"6-review.md": "good\n\nVERDICT: APPROVED\n",
	})

	runs, err := listRuns(state)
	if err != nil {
		t.Fatalf("listRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].verdict != "APPROVED" {
		t.Errorf("verdict = %+v, want a single APPROVED run", runs)
	}
}

func TestListRunsMissingStateDir(t *testing.T) {
	runs, err := listRuns(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("listRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("got %d runs, want none", len(runs))
	}
}

func TestRunArtifactsOrder(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"2-plan-review.md", "1-plan.log", "10-fix.md", "1-plan.md", "verify.log"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	arts, err := runArtifacts(dir)
	if err != nil {
		t.Fatalf("runArtifacts: %v", err)
	}
	var got []string
	for _, a := range arts {
		got = append(got, a.name)
	}
	want := []string{"1-plan.log", "1-plan.md", "2-plan-review.md", "10-fix.md", "verify.log"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("artifacts = %v, want %v", got, want)
	}
}

func TestStageNumber(t *testing.T) {
	tests := map[string]int{
		"1-plan.md":  1,
		"10-fix.md":  10,
		"verify.log": 0,
		"-weird":     0,
		"plan.md":    0,
	}
	for name, want := range tests {
		if got := stageNumber(name); got != want {
			t.Errorf("stageNumber(%q) = %d, want %d", name, got, want)
		}
	}
}

func TestValidRunID(t *testing.T) {
	for _, id := range []string{"20260101-120000", "run-1"} {
		if err := validRunID(id); err != nil {
			t.Errorf("validRunID(%q) = %v, want nil", id, err)
		}
	}
	for _, id := range []string{"", ".", "..", "a/b", "/abs", `..\x`, "nested/run"} {
		if err := validRunID(id); err == nil {
			t.Errorf("validRunID(%q) = nil, want an error", id)
		}
	}
}

func TestCmdStatus(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeRun(t, filepath.Join(dir, buildStateDir), "20260101-120000", map[string]string{
		"1-plan.md":   "plan",
		"4-review.md": "ok\n\nVERDICT: APPROVED\n",
	})

	var stdout, stderr bytes.Buffer
	if code := cmdStatus(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("cmdStatus = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "20260101-120000") || !strings.Contains(out, "APPROVED") {
		t.Errorf("status output missing run or verdict:\n%s", out)
	}
}

func TestCmdStatusNoRuns(t *testing.T) {
	t.Chdir(t.TempDir())

	var stdout, stderr bytes.Buffer
	if code := cmdStatus(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("cmdStatus = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no runs") {
		t.Errorf("status output = %q, want a no-runs message", stdout.String())
	}
}

func TestCmdStatusRejectsArgs(t *testing.T) {
	t.Chdir(t.TempDir())

	var stdout, stderr bytes.Buffer
	if code := cmdStatus([]string{"20260101-120000"}, &stdout, &stderr); code != 2 {
		t.Errorf("cmdStatus with an argument = %d, want 2", code)
	}
}

func TestCmdLogs(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeRun(t, filepath.Join(dir, buildStateDir), "20260101-120000", map[string]string{
		"1-plan.md":  "plan",
		"1-plan.log": "transcript",
	})

	var stdout, stderr bytes.Buffer
	if code := cmdLogs([]string{"20260101-120000"}, &stdout, &stderr); code != 0 {
		t.Fatalf("cmdLogs = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "1-plan.md") || !strings.Contains(out, "1-plan.log") {
		t.Errorf("logs output missing artifacts:\n%s", out)
	}
}

func TestCmdLogsInvalidRunID(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll(filepath.Join(dir, buildStateDir, "20260101-120000"), 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}

	tests := []struct {
		name string
		args []string
		want int
	}{
		{"missing id", nil, 2},
		{"unknown id", []string{"20250101-000000"}, 2},
		{"traversal", []string{"../../etc"}, 2},
		{"separator", []string{"nested/run"}, 2},
		{"two ids", []string{"a", "b"}, 2},
		{"unknown flag", []string{"--nope"}, 2},
		{"help", []string{"-h"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := cmdLogs(tt.args, &stdout, &stderr); code != tt.want {
				t.Errorf("cmdLogs(%q) = %d, want %d (stderr: %s)", tt.args, code, tt.want, stderr.String())
			}
		})
	}
}

// TestOrchMainStatusLogs checks the dispatch wiring end to end through orchMain.
func TestOrchMainStatusLogs(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeRun(t, filepath.Join(dir, buildStateDir), "20260101-120000", map[string]string{
		"1-plan.md": "plan",
	})

	var stdout, stderr bytes.Buffer
	if code := orchMain([]string{"status"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("orchMain(status) = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "20260101-120000") {
		t.Errorf("status output = %q, want the run id", stdout.String())
	}

	stdout.Reset()
	if code := orchMain([]string{"logs", "20260101-120000"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("orchMain(logs) = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "1-plan.md") {
		t.Errorf("logs output = %q, want the artifact listing", stdout.String())
	}
}
