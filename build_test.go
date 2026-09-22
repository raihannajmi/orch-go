package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
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

	stage := interactiveStage(dir, runDir, nil, io.Discard, defaultStageTimeout)
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

	stage := interactiveStage(dir, runDir, nil, io.Discard, defaultStageTimeout)
	if _, err := stage("agy", "1-plan", "prompt"); err == nil {
		t.Fatal("stage = nil error, want a missing-artifact error")
	}
}

// TestInteractiveStageEndsLingeringAgent is the regression test for the
// orchestration bug: an agent that writes its artifact and then stays at its
// interactive prompt must not stall the workflow. orch sees the completion
// marker, ends the session, and returns the artifact.
func TestInteractiveStageEndsLingeringAgent(t *testing.T) {
	dir := t.TempDir()
	runDir := t.TempDir()
	artifact := filepath.Join(runDir, "1-plan.md")

	binDir := t.TempDir()
	// Stand-in agent: finish the requested work, then keep the session open the
	// way a real interactive agent does after replying.
	script := "#!/bin/sh\n" +
		"cat > '" + artifact + "' <<'EOF'\nplanned\n" + stageMarker + "\nEOF\n" +
		"sleep 3600\n"
	if err := os.WriteFile(filepath.Join(binDir, "agy"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stand-in agent: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// A generous watchdog: this stage completes well before it, proving the
	// timeout does not disturb the normal completion path.
	stage := interactiveStage(dir, runDir, nil, io.Discard, 30*time.Second)

	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		text, err := stage("agy", "1-plan", "prompt")
		done <- result{text, err}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("stage: %v (orch should end the agent's session once the artifact is complete)", r.err)
		}
		if r.text != "planned" {
			t.Errorf("stage text = %q, want %q", r.text, "planned")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("stage did not return: orch waited on an agent that finished its work and idled")
	}
}

func TestArtifactComplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.md")
	if artifactComplete(path) {
		t.Error("missing artifact reported complete")
	}

	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatalf("write artifact: %v", err)
		}
	}

	write("still writing")
	if artifactComplete(path) {
		t.Error("artifact without the marker reported complete")
	}
	write(stageMarker + "\nmore content\n")
	if artifactComplete(path) {
		t.Error("marker that is not the last line reported complete")
	}
	write("body\n\n" + stageMarker + "\n")
	if !artifactComplete(path) {
		t.Error("artifact ending in the marker not reported complete")
	}
}

func TestStripStageMarker(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"body\n" + stageMarker + "\n", "body"},
		{"body\n\n  " + stageMarker + "  \n", "body"},
		{"body", "body"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := stripStageMarker(tt.in); got != tt.want {
			t.Errorf("stripStageMarker(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestStagePromptsRequireMarker guards requirement 9: every stage — plan, plan
// review, implement, review and fix — must carry the completion signal, not
// just the ones a change happened to touch.
func TestStagePromptsRequireMarker(t *testing.T) {
	body := contextDoc{"context", "/ctx.md", "content"}
	prompts := map[string]string{
		"plan":        planPrompt("task", "/repo", "/out.md"),
		"plan-review": planReviewPrompt("task", "/repo", "/out.md", body),
		"implement":   implementPrompt("task", "/repo", "/out.md", body, body),
		"review":      reviewPrompt("task", "/repo", "/out.md", body, body),
		"fix":         fixPrompt("task", "/repo", "/out.md", body, body),
	}
	for name, p := range prompts {
		if !strings.Contains(p, stageMarker) {
			t.Errorf("%s prompt does not require the completion marker", name)
		}
	}
}

// TestInteractiveStageTimesOut is the regression test for a wedged stage: an
// agent that neither writes its completion marker nor exits must be ended by the
// watchdog, not waited on forever. It also checks the cleanup: the timeout is
// recorded for `orch status`, the agent process is reaped, and the goroutines
// the stage started are all gone.
func TestInteractiveStageTimesOut(t *testing.T) {
	dir := t.TempDir()
	runDir := t.TempDir()

	binDir := t.TempDir()
	pidFile := filepath.Join(dir, "agent.pid")
	// Stand-in agent: record its pid, never write the artifact, never exit —
	// the shape of a wedged prompt or a hung tool call.
	script := "#!/bin/sh\necho $$ > '" + pidFile + "'\nsleep 3600\n"
	if err := os.WriteFile(filepath.Join(binDir, "agy"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stand-in agent: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	warmSignalLoop()
	before := runtime.NumGoroutine()
	stage := interactiveStage(dir, runDir, nil, io.Discard, time.Second)

	start := time.Now()
	_, err := stage("agy", "1-plan", "prompt")
	if !errors.Is(err, errStageTimeout) {
		t.Fatalf("stage error = %v, want errStageTimeout", err)
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Errorf("stage took %s, want the watchdog to end it promptly", d)
	}

	if _, statErr := os.Stat(timeoutMarkerPath(runDir, "1-plan")); statErr != nil {
		t.Errorf("timeout marker not written: %v", statErr)
	}

	if pid := readPID(t, pidFile); processAlive(pid) {
		t.Errorf("agent process %d is still alive after the timeout", pid)
	}

	waitForGoroutines(t, before)
}

// TestBuildStopsAfterStageTimeout covers cancellation: once a stage times out,
// the workflow must stop rather than start the next one.
func TestBuildStopsAfterStageTimeout(t *testing.T) {
	b, calls, _, verifications := fakeBuilder(t, 1)

	inner := b.stage
	b.stage = func(agentName, name, prompt string) (string, error) {
		if name == "1-plan" {
			*calls = append(*calls, name)
			return "", fmt.Errorf("stage %s: %w", name, errStageTimeout)
		}
		return inner(agentName, name, prompt)
	}

	err := b.build("do the thing")
	if !errors.Is(err, errStageTimeout) {
		t.Fatalf("build error = %v, want errStageTimeout", err)
	}
	if want := "1-plan"; strings.Join(*calls, ",") != want {
		t.Errorf("stages = %v, want only %q (no stage may start after a timeout)", *calls, want)
	}
	if *verifications != 0 {
		t.Errorf("verification ran %d times, want 0 after a timeout", *verifications)
	}
}

func TestParseBuildArgsStageTimeout(t *testing.T) {
	opts, err := parseBuildArgs([]string{"task"})
	if err != nil {
		t.Fatalf("parseBuildArgs: %v", err)
	}
	if opts.stageTimeout != defaultStageTimeout {
		t.Errorf("default stage timeout = %s, want %s", opts.stageTimeout, defaultStageTimeout)
	}

	if opts, err = parseBuildArgs([]string{"--stage-timeout", "45s", "task"}); err != nil {
		t.Fatalf("parseBuildArgs: %v", err)
	}
	if opts.stageTimeout != 45*time.Second {
		t.Errorf("stage timeout = %s, want 45s", opts.stageTimeout)
	}

	if opts, err = parseBuildArgs([]string{"--stage-timeout=0", "task"}); err != nil {
		t.Fatalf("parseBuildArgs: %v", err)
	}
	if opts.stageTimeout != 0 {
		t.Errorf("stage timeout = %s, want 0 (disabled)", opts.stageTimeout)
	}

	for _, args := range [][]string{
		{"--stage-timeout", "nope", "task"},
		{"--stage-timeout", "-5s", "task"},
		{"--stage-timeout"},
	} {
		if _, err := parseBuildArgs(args); err == nil {
			t.Errorf("parseBuildArgs(%q) = nil error, want a usage error", args)
		}
	}
}

// readPID waits for the stand-in agent to record its pid.
func readPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(path); err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(b))); convErr == nil {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent did not record its pid in %s", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// processAlive reports whether pid still exists.
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// warmSignalLoop starts the process-wide os/signal watcher once, so a goroutine
// baseline taken afterwards is not inflated by the first Run that calls
// signal.Notify. The watcher goroutine is created once and never stops.
func warmSignalLoop() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	signal.Stop(ch)
}

// waitForGoroutines waits for the goroutine count to fall back to want, failing
// if it does not: a session that leaves a reader or a watcher behind shows up
// here.
func waitForGoroutines(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := runtime.NumGoroutine(); got <= want {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("goroutines leaked: %d still running, want <= %d", runtime.NumGoroutine(), want)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
