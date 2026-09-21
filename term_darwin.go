//go:build darwin

package main

import (
	"syscall"
	"unsafe"
)

// rawMode puts fd into raw mode and returns the previous termios settings so
// they can be restored. It is the darwin equivalent of term.MakeRaw, written
// against the standard library so orch keeps a single dependency (creack/pty).
func rawMode(fd int) (*syscall.Termios, error) {
	old, err := getTermios(fd)
	if err != nil {
		return nil, err
	}

	t := *old
	t.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP |
		syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	t.Oflag &^= syscall.OPOST
	t.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	t.Cflag &^= syscall.CSIZE | syscall.PARENB
	t.Cflag |= syscall.CS8
	t.Cc[syscall.VMIN] = 1
	t.Cc[syscall.VTIME] = 0

	if err := setTermios(fd, &t); err != nil {
		return nil, err
	}
	return old, nil
}

// restoreTermios puts back settings previously returned by rawMode.
func restoreTermios(fd int, t *syscall.Termios) error {
	return setTermios(fd, t)
}

func getTermios(fd int) (*syscall.Termios, error) {
	var t syscall.Termios
	if err := ioctl(fd, syscall.TIOCGETA, unsafe.Pointer(&t)); err != nil {
		return nil, err
	}
	return &t, nil
}

func setTermios(fd int, t *syscall.Termios) error {
	return ioctl(fd, syscall.TIOCSETA, unsafe.Pointer(t))
}

func ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}
