//go:build !darwin

package main

import (
	"errors"
	"syscall"
)

// orch targets macOS. Off darwin the terminal is left alone, so Run still works
// with piped input but an agent's full-screen UI will not be driven correctly.
var errNoRawMode = errors.New("raw mode is only implemented on darwin")

func rawMode(int) (*syscall.Termios, error) {
	return nil, errNoRawMode
}

func restoreTermios(int, *syscall.Termios) error {
	return errNoRawMode
}
