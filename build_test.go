package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBuilder returns a builder whose stage and verify steps are recorded rather
// than launched, so the workflow's sequencing can be tested on its own.
func fakeBuilder(t *testing.T, approveOn int) (*builder, *[]string, *map[string]string, *int) {
	t.Helper()

	calls := new([]string)
	prompts := new(map[string]string)
	*prompts = map[string]string{}
	reviews := 0
	verifications := new(int)

	b := &builder{
		dir:    "/repo",
		runDir: t.TempDir(),
		stdout: io.Discard,
		stderr: io.Discard,
	}
	b.stage = func(agentName, name, prompt string) (string, error) {
		*calls = append(*calls, name)
		(*prompts)[name] = prompt

		// The plan review also comes from the reviewer agent, but it is not a
		// gate; only the implementation reviews decide the loop.
		if agentName != buildReviewAgent || name == "2-plan-review" {
			return name + " output", nil
		}
		reviews++
		if reviews >= approveOn {
			return "Looks correct.\n\nVERDICT: APPROVED", nil
		}
		return "Missing pieces.\n\nVERDICT: REJECTED", nil
	}
	b.verify = func() error { *verifications++; return nil }
	return b, calls, prompts, verifications
}

func TestBuildWorkflowStopsWhenApproved(t *testing.T) {
	b, calls, prompts, verifications := fakeBuilder(t, 1)

	if err := b.build("add a --json flag"); err != nil {
		t.Fatalf("build: %v", err)
	}

	want := []string{"1-plan", "2-plan-review", "3-implement", "4-review"}
	if strings.Join(*calls, ",") != strings.Join(want, ",") {
		t.Errorf("stages = %v, want %v", *calls, want)
	}
	if *verifications != 1 {
		t.Errorf("verification ran %d times, want 1", *verifications)
	}
	// Context must flow forward: the implementer sees the plan and its review.
	impl := (*prompts)["3-implement"]
	if !strings.Contains(impl, "1-plan output") || !strings.Contains(impl, "2-plan-review output") {
		t.Errorf("implement prompt missing prior stage context:\n%s", impl)
	}
}

func TestBuildWorkflowFixesUntilApproved(t *testing.T) {
	b, calls, prompts, verifications := fakeBuilder(t, 2)

	if err := b.build("do the thing"); err != nil {
		t.Fatalf("build: %v", err)
	}

	want := []string{"1-plan", "2-plan-review", "3-implement", "4-review", "5-fix", "6-review"}
	if strings.Join(*calls, ",") != strings.Join(want, ",") {
		t.Errorf("stages = %v, want %v", *calls, want)
	}
	if *verifications != 1 {
		t.Errorf("verification ran %d times, want 1", *verifications)
	}
	// The fix must be handed the rejecting review.
	fix := (*prompts)["5-fix"]
	if !strings.Contains(fix, "VERDICT: REJECTED") {
		t.Errorf("fix prompt missing the previous review:\n%s", fix)
	}
}

func TestBuildWorkflowGivesUpAfterMaxCycles(t *testing.T) {
	b, calls, _, verifications := fakeBuilder(t, 99)

	err := b.build("never good enough")
	if err == nil {
		t.Fatal("build succeeded, want a give-up error after max cycles")
	}
	if !strings.Contains(err.Error(), "not approved") {
		t.Errorf("error = %q, want mention of no approval", err)
	}
	// plan + plan-review + 3 × (implement/fix + review)
	if want := 2 + 2*buildPlanCycles; len(*calls) != want {
		t.Errorf("ran %d stages (%v), want %d", len(*calls), *calls, want)
	}
	if *verifications != 0 {
		t.Errorf("verification ran %d times, want 0 when the work is rejected", *verifications)
	}
}

func TestReviewApproved(t *testing.T) {
	tests := []struct {
		review string
		want   bool
	}{
		{"VERDICT: APPROVED", true},
		{"blah\nVERDICT: REJECTED", false},
		{"blah\nVERDICT: REJECTED\nmore\nVERDICT: APPROVED", true}, // last verdict wins
		{"no verdict here", false},
		{"verdict: approved", true}, // case-insensitive
		{"VERDICT: APPROVEDNESS", false},
	}
	for _, tt := range tests {
		if got := reviewApproved(tt.review); got != tt.want {
			t.Errorf("reviewApproved(%q) = %v, want %v", tt.review, got, tt.want)
		}
	}
}

func TestParseBuildArgs(t *testing.T) {
	got, err := parseBuildArgs([]string{"-C", "/tmp/repo", "add", "a", "flag"})
	if err != nil {
		t.Fatalf("parseBuildArgs: %v", err)
	}
	if got.dir != "/tmp/repo" {
		t.Errorf("dir = %q, want /tmp/repo", got.dir)
	}
	if got.task != "add a flag" {
		t.Errorf("task = %q, want %q", got.task, "add a flag")
	}

	joined, err := parseBuildArgs([]string{"--dir=/srv", `fix "the" bug`})
	if err != nil {
		t.Fatalf("parseBuildArgs: %v", err)
	}
	if joined.dir != "/srv" || joined.task != `fix "the" bug` {
		t.Errorf("got %+v, want dir /srv task fix \"the\" bug", joined)
	}

	for _, args := range [][]string{{"--nope", "x"}, {"-C"}} {
		if _, err := parseBuildArgs(args); err == nil {
			t.Errorf("parseBuildArgs(%q) = nil error, want a usage error", args)
		}
	}
	if _, err := parseBuildArgs([]string{"-h"}); err != errHelp {
		t.Errorf("parseBuildArgs(-h) = %v, want errHelp", err)
	}
}

func TestOrchMainBuild(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"build without a task", []string{"build"}, 2},
		{"build help", []string{"build", "-h"}, 0},
		{"build unknown flag", []string{"build", "--nope", "x"}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := orchMain(tt.args, nil, &stdout, &stderr); got != tt.want {
				t.Errorf("orchMain(%q) = %d, want %d (stderr: %s)", tt.args, got, tt.want, stderr.String())
			}
		})
	}
}

// TestInteractiveStageReadsArtifact checks that a stage surfaces the artifact an
// agent wrote, standing in for the agy binary with a script on PATH.
func TestInteractiveStageReadsArtifact(t *testing.T) {
	dir := t.TempDir()
	runDir := t.TempDir()

	binDir := t.TempDir()
	artifact := filepath.Join(runDir, "1-plan.md")
	script := "#!/bin/sh\nprintf planned > '" + artifact + "'\n"
	if err := os.WriteFile(filepath.Join(binDir, "agy"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stand-in agent: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stage := interactiveStage(dir, runDir, nil, io.Discard)
	text, err := stage("agy", "1-plan", "prompt")
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if text != "planned" {
		t.Errorf("stage text = %q, want %q", text, "planned")
	}
	if _, err := os.Stat(filepath.Join(runDir, "1-plan.log")); err != nil {
		t.Errorf("transcript log was not written: %v", err)
	}
}

// TestInteractiveStageMissingArtifact ensures a stage fails loudly rather than
// fabricating output when the agent never writes its artifact.
func TestInteractiveStageMissingArtifact(t *testing.T) {
	dir := t.TempDir()
	runDir := t.TempDir()

	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "agy"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write stand-in agent: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stage := interactiveStage(dir, runDir, nil, io.Discard)
	if _, err := stage("agy", "1-plan", "prompt"); err == nil {
		t.Fatal("stage = nil error, want a missing-artifact error")
	}
}
