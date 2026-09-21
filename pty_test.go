package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

// runSession runs a session with a deadline so a pty wiring bug fails the test
// instead of hanging it.
func runSession(t *testing.T, argv []string, opts Options) (int, error) {
	t.Helper()

	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		code, err := Run(argv, opts)
		done <- result{code, err}
	}()

	select {
	case r := <-done:
		return r.code, r.err
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return: the pty session is stuck")
		return 0, nil
	}
}

// TestRunProvidesRealPTY is the core promise: the agent sees a genuine
// controlling terminal on stdin, stdout, and stderr.
func TestRunProvidesRealPTY(t *testing.T) {
	var out bytes.Buffer
	script := `p=$(tty); if [ -t 0 ] && [ -t 1 ] && [ -t 2 ]; then echo yespty:$p; else echo nopty; fi`

	code, err := runSession(t, []string{"/bin/sh", "-c", script}, Options{Stdout: &out})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if got := out.String(); !strings.Contains(got, "yespty:/dev/") {
		t.Errorf("agent did not get a controlling terminal, output = %q", got)
	}
}

func TestRunPropagatesExitCode(t *testing.T) {
	code, err := runSession(t, []string{"/bin/sh", "-c", "exit 3"}, Options{Stdout: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
}

func TestRunReportsKilledAgent(t *testing.T) {
	code, err := runSession(t, []string{"/bin/sh", "-c", "kill -TERM $$"}, Options{Stdout: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 128+15 {
		t.Errorf("exit code = %d, want %d", code, 128+15)
	}
}

// TestRunForwardsInput covers the non-terminal path: a pipe stands in for the
// keyboard, and raw mode and sizing are skipped rather than failing.
func TestRunForwardsInput(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = r.Close() }()

	if _, err := w.WriteString("ping\n"); err != nil {
		t.Fatalf("write to pipe: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}

	var out bytes.Buffer
	code, err := runSession(t, []string{"/bin/sh", "-c", `read line; echo got:$line`}, Options{Stdin: r, Stdout: &out})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if got := out.String(); !strings.Contains(got, "got:ping") {
		t.Errorf("keystrokes were not forwarded, output = %q", got)
	}
}

func TestRunReportsStartFailure(t *testing.T) {
	code, err := runSession(t, []string{"/nonexistent/orch-test-agent"}, Options{Stdout: &bytes.Buffer{}})
	if err == nil {
		t.Fatal("Run = nil error, want a start failure")
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0 alongside the error", code)
	}
}

func TestRunRejectsEmptyCommand(t *testing.T) {
	if _, err := Run(nil, Options{}); err == nil {
		t.Fatal("Run(nil) = nil error, want an error")
	}
}

// TestRunStopEndsSession covers the mechanism that unblocks the build workflow:
// a caller that can tell the agent is done closes Stop, and Run ends the still
// running session instead of waiting for it to exit on its own.
func TestRunStopEndsSession(t *testing.T) {
	dir := t.TempDir()
	ready := dir + "/ready"

	stop := make(chan struct{})
	go func() {
		for {
			if _, err := os.Stat(ready); err == nil {
				close(stop)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()

	script := "touch '" + ready + "'\nsleep 3600"
	code, err := runSession(t, []string{"/bin/sh", "-c", script}, Options{Stdout: &bytes.Buffer{}, Stop: stop})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code == 0 {
		t.Errorf("exit code = %d, want the terminated agent's non-zero status", code)
	}
}
