//go:build darwin

package main

import (
	"syscall"
	"testing"

	"github.com/creack/pty"
)

// TestRawModeRoundTrip drives the termios ioctl against a real pty, so the
// struct layout and ioctl constants are checked by the kernel instead of being
// taken on trust. This is the part of orch that has to be exactly right for an
// agent's full-screen UI to receive keystrokes.
func TestRawModeRoundTrip(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer func() { _ = ptmx.Close() }()
	defer func() { _ = tty.Close() }()

	fd := int(tty.Fd())
	before, err := getTermios(fd)
	if err != nil {
		t.Fatalf("getTermios: %v", err)
	}
	if before.Lflag&syscall.ICANON == 0 || before.Lflag&syscall.ECHO == 0 {
		t.Fatalf("expected a cooked terminal before raw mode, got Lflag %#x", before.Lflag)
	}

	old, err := rawMode(fd)
	if err != nil {
		t.Fatalf("rawMode: %v", err)
	}
	if old.Lflag != before.Lflag {
		t.Errorf("rawMode reported Lflag %#x, want the previous %#x", old.Lflag, before.Lflag)
	}

	raw, err := getTermios(fd)
	if err != nil {
		t.Fatalf("getTermios after rawMode: %v", err)
	}
	if raw.Lflag&(syscall.ICANON|syscall.ECHO|syscall.ISIG) != 0 {
		t.Errorf("Lflag %#x still has ICANON/ECHO/ISIG set", raw.Lflag)
	}
	if raw.Oflag&syscall.OPOST != 0 {
		t.Errorf("Oflag %#x still has OPOST set", raw.Oflag)
	}
	if raw.Cflag&syscall.CSIZE != syscall.CS8 {
		t.Errorf("Cflag %#x does not select CS8", raw.Cflag)
	}
	if raw.Cc[syscall.VMIN] != 1 || raw.Cc[syscall.VTIME] != 0 {
		t.Errorf("VMIN/VTIME = %d/%d, want 1/0", raw.Cc[syscall.VMIN], raw.Cc[syscall.VTIME])
	}

	if err := restoreTermios(fd, old); err != nil {
		t.Fatalf("restoreTermios: %v", err)
	}
	after, err := getTermios(fd)
	if err != nil {
		t.Fatalf("getTermios after restore: %v", err)
	}
	if *after != *before {
		t.Errorf("restore left %+v, want %+v", *after, *before)
	}
}
