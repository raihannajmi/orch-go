package main

import "golang.org/x/term"

// rawMode puts fd into raw mode and returns the previous state so it can be
// restored. It delegates to golang.org/x/term, which applies the same cfmakeraw
// flags orch used to hand-roll for darwin but now does so on every platform it
// targets (macOS, Linux, the BSDs, Solaris, Windows). Anywhere x/term cannot
// drive the terminal it returns that error rather than leaving the terminal
// cooked, so an interactive agent is never launched against a tty that would
// swallow its keystrokes.
//
// Callers must check term.IsTerminal first: a pipe standing in for the keyboard
// makes MakeRaw return an ioctl error, which means "forward bytes only", not a
// platform failure.
func rawMode(fd int) (*term.State, error) {
	return term.MakeRaw(fd)
}

// restoreTermios puts back settings previously returned by rawMode.
func restoreTermios(fd int, state *term.State) error {
	return term.Restore(fd, state)
}
