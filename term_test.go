package main

import (
	"io"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

type readResult struct {
	b   byte
	n   int
	err error
}

// startRead reads one line (up to a small buffer) in the background so the test
// can bound how long it waits without leaving a goroutine blocked on a terminal
// that is deliberately withholding input. Callers must release a pending read by
// completing the line before starting the next one.
func startRead(r *os.File) <-chan readResult {
	ch := make(chan readResult, 1)
	go func() {
		buf := make([]byte, 16)
		n, err := r.Read(buf)
		var b byte
		if n > 0 {
			b = buf[0]
		}
		ch <- readResult{b, n, err}
	}()
	return ch
}

// waitRead reports the first byte of the read, or ok=false if none arrived
// within d. A read error fails the test.
func waitRead(t *testing.T, ch <-chan readResult, d time.Duration) (byte, bool) {
	t.Helper()
	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("read: %v", res.err)
		}
		if res.n == 0 {
			return 0, false
		}
		return res.b, true
	case <-time.After(d):
		return 0, false
	}
}

func writeByte(t *testing.T, w *os.File, s string) {
	t.Helper()
	if _, err := w.WriteString(s); err != nil {
		t.Fatalf("write %q: %v", s, err)
	}
}

// TestRawModeRoundTrip switches a real pty's slave into raw mode and back,
// asserting the kernel-visible effect on both sides of the round trip: a cooked
// terminal withholds a byte until a newline arrives, raw mode releases it at
// once, and restore reintroduces line buffering. Driving it through a real pty
// checks orch's wiring and golang.org/x/term underneath it on every platform,
// rather than trusting a hand-rolled termios struct layout the way the old
// darwin-only test did.
func TestRawModeRoundTrip(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer func() { _ = ptmx.Close() }()
	defer func() { _ = tty.Close() }()

	fd := int(tty.Fd())
	if !term.IsTerminal(fd) {
		t.Fatalf("pty slave fd %d is not reported as a terminal", fd)
	}

	// Cooked: a byte with no newline is withheld until the line is complete.
	ch := startRead(tty)
	writeByte(t, ptmx, "c")
	if _, ok := waitRead(t, ch, 200*time.Millisecond); ok {
		t.Fatal("cooked terminal delivered a byte before a newline arrived")
	}
	writeByte(t, ptmx, "\n")
	if _, ok := waitRead(t, ch, 2*time.Second); !ok {
		t.Fatal("cooked terminal did not deliver the completed line")
	}

	saved, err := rawMode(fd)
	if err != nil {
		t.Fatalf("rawMode: %v", err)
	}
	if saved == nil {
		t.Fatal("rawMode returned a nil state")
	}

	// Raw: the same single byte arrives immediately, newline or not.
	ch = startRead(tty)
	writeByte(t, ptmx, "r")
	if b, ok := waitRead(t, ch, 2*time.Second); !ok || b != 'r' {
		t.Errorf("raw terminal read %q (ok=%v), want 'r'", b, ok)
	}

	if err := restoreTermios(fd, saved); err != nil {
		t.Fatalf("restoreTermios: %v", err)
	}

	// Cooked again: restore must put the line discipline back the way it was.
	ch = startRead(tty)
	writeByte(t, ptmx, "z")
	if _, ok := waitRead(t, ch, 200*time.Millisecond); ok {
		t.Error("restoreTermios did not return the terminal to cooked mode")
	}
	writeByte(t, ptmx, "\n")
	waitRead(t, ch, 2*time.Second)
}

// TestRawModeRejectsNonTerminal covers the piped-stdin path: a pipe standing in
// for the keyboard has no terminal to switch, so rawMode reports an error and
// Run forwards bytes without touching it.
func TestRawModeRejectsNonTerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()

	if term.IsTerminal(int(r.Fd())) {
		t.Fatal("a pipe reported itself as a terminal")
	}
	if _, err := rawMode(int(r.Fd())); err == nil {
		t.Error("rawMode on a pipe = nil error, want one")
	}
}

// TestRunInteractiveSessionCleansUp drives Run with a real terminal on stdin (a
// pty slave standing in for the keyboard), so raw mode, resize handling and the
// input forwarder all run. When the agent exits, the forwarder must be released
// rather than left parked on stdin, and the terminal must be restored.
func TestRunInteractiveSessionCleansUp(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer func() { _ = ptmx.Close() }()
	defer func() { _ = tty.Close() }()

	warmSignalLoop()
	before := runtime.NumGoroutine()

	code, err := runSession(t, []string{"/bin/sh", "-c", "exit 0"}, Options{Stdin: tty, Stdout: io.Discard})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	waitForGoroutines(t, before)
}
