package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// stopGracePeriod is how long a session may linger after orch asks it to end
// before it is killed outright, so a wedged UI cannot stall a workflow.
const stopGracePeriod = 2 * time.Second

// inputPollInterval bounds how long forwardInput waits for a keystroke before it
// re-checks whether the session has ended, so the reader is released when the
// session ends instead of parking on the terminal forever.
const inputPollInterval = 250 * time.Millisecond

// errPollTimeout reports that no input arrived within the poll wait.
var errPollTimeout = errors.New("no input ready")

// Options configures one interactive agent session.
type Options struct {
	// Dir is the agent's working directory; empty inherits orch's.
	Dir string
	// Stdin is orch's terminal. It is switched to raw mode and its keystrokes are
	// forwarded to the agent. May be nil, or a non-terminal, when orchestrating
	// without a tty (piped input, tests); sizing and raw mode are then skipped and
	// only the bytes that arrive are forwarded.
	Stdin *os.File
	// Stdout receives the agent's terminal output. Defaults to os.Stdout.
	Stdout io.Writer
	// Stop, when non-nil, ends the session on demand: closing it terminates the
	// agent's process group (SIGTERM, then SIGKILL after stopGracePeriod). The
	// build workflow uses it once a stage's artifact is complete, because an
	// interactive agent finishes its work but stays at its prompt. Nothing is
	// filtered while the session runs, so native permission prompts are intact.
	Stop <-chan struct{}
}

// Run starts argv on a new pseudo-terminal wired to the caller's terminal and
// returns the agent's exit code.
//
// Nothing about the agent's terminal behaviour is emulated or filtered: it
// receives a real pty as its controlling terminal, so its native UI, redraws,
// and permission prompts work exactly as they do when launched directly.
func Run(argv []string, opts Options) (int, error) {
	if len(argv) == 0 {
		return 0, errors.New("no command given")
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = opts.Dir

	// Size the pty before the agent starts so a full-screen UI can read the real
	// dimensions on its first draw instead of racing a later resize.
	var size *pty.Winsize
	if opts.Stdin != nil {
		if ws, err := pty.GetsizeFull(opts.Stdin); err == nil {
			size = ws
		}
	}

	interactive := opts.Stdin != nil

	// The agent owns the line discipline from here on: raw mode stops the local
	// terminal from consuming arrow keys, control characters, and permission-
	// prompt keystrokes on the way through. It is switched before the agent
	// starts so a failure can be reported without a live process to clean up. A
	// piped stdin is not a terminal, so there is nothing to switch and keystrokes
	// are merely forwarded; a real terminal that cannot go raw is a platform orch
	// does not support, and that fails here rather than silently dropping input.
	if interactive && term.IsTerminal(int(opts.Stdin.Fd())) {
		saved, err := rawMode(int(opts.Stdin.Fd()))
		if err != nil {
			return 0, fmt.Errorf("raw mode: %w", err)
		}
		defer func() { _ = restoreTermios(int(opts.Stdin.Fd()), saved) }()
	}

	// Start puts the agent in its own session with the pty as controlling terminal.
	ptmx, err := pty.StartWithSize(cmd, size)
	if err != nil {
		if errors.Is(err, syscall.E2BIG) {
			return 0, fmt.Errorf("cannot start %s: the command line is too long for the OS; shorten the task or reduce inlined context: %w", argv[0], err)
		}
		return 0, fmt.Errorf("start %s: %w", argv[0], err)
	}
	defer func() { _ = ptmx.Close() }()

	// Follow terminal resizes so the agent redraws at the right size.
	winch := make(chan os.Signal, 1)
	if interactive {
		signal.Notify(winch, syscall.SIGWINCH)
		go func() {
			for range winch {
				_ = pty.InheritSize(opts.Stdin, ptmx)
			}
		}()
	}
	defer func() {
		signal.Stop(winch)
		close(winch)
	}()

	// While the terminal is raw, Ctrl-C is an ordinary byte: it is forwarded to
	// the pty and raised as SIGINT by the agent's own terminal, so the agent
	// decides what it means. These handlers cover the cases where orch itself is
	// signalled (piped stdin, a closing window) so agents are never orphaned.
	terms := make(chan os.Signal, 1)
	signal.Notify(terms, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for sig := range terms {
			s, ok := sig.(syscall.Signal)
			if !ok || cmd.Process == nil {
				continue
			}
			// The agent leads its own session (pty.Start), so its process group id
			// equals its pid; signalling the group takes its children with it.
			_ = syscall.Kill(-cmd.Process.Pid, s)
		}
	}()
	defer func() {
		signal.Stop(terms)
		close(terms)
	}()

	// Drain the agent's output until it is gone. Closing the master after Wait
	// releases the copy even if a grandchild is still holding the tty open.
	output := make(chan struct{})
	go func() {
		defer close(output)
		_, _ = io.Copy(opts.Stdout, ptmx)
	}()

	if interactive {
		// The fd is resolved here, not in the goroutine: reading it after Run
		// returns would race a caller closing its stdin file.
		go forwardInput(int(opts.Stdin.Fd()), ptmx, output)
	}

	// A caller that knows the agent is done (its artifact is complete) can ask
	// the session to end rather than waiting for an interactive prompt that may
	// never close on its own.
	sessionDone := make(chan struct{})
	if opts.Stop != nil {
		go stopSession(cmd, opts.Stop, sessionDone)
	}

	waitErr := cmd.Wait()
	close(sessionDone)
	_ = ptmx.Close()
	<-output

	return exitCode(waitErr)
}

// stopSession ends a session whose agent has finished its work but lingers at
// its prompt. Closing the caller's channel signals the agent's process group to
// exit; a SIGKILL follows if it is still alive after stopGracePeriod, so a
// wedged UI cannot stall the workflow. It returns once the agent is gone.
func stopSession(cmd *exec.Cmd, stop, sessionDone <-chan struct{}) {
	select {
	case <-stop:
	case <-sessionDone:
		return
	}
	killGroup(cmd, syscall.SIGTERM)
	select {
	case <-sessionDone:
	case <-time.After(stopGracePeriod):
		killGroup(cmd, syscall.SIGKILL)
	}
}

// killGroup signals the agent's whole process group. The agent leads its own
// session (pty.Start), so its group id equals its pid and children follow.
func killGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, sig)
}

// forwardInput copies keystrokes from the terminal fd to the pty until the
// session ends or stdin goes away.
//
// It polls stdin rather than blocking in a bare Read so that closing done —
// which happens when the agent is gone — releases this goroutine at once.
// Without that, every stage of a build would leave a reader parked on the
// terminal until the next keystroke.
//
// ponytail: a keystroke typed in the instant between one agent exiting and the
// next starting can be dropped (it never reaches the wrong agent, since the
// write fails once the pty is closed). Polling stdin and this channel together
// would close that gap; not worth the machinery while a session owns the
// terminal alone.
func forwardInput(fd int, ptmx *os.File, done <-chan struct{}) {
	buf := make([]byte, 4096)
	for {
		select {
		case <-done:
			return
		default:
		}
		n, err := pollRead(fd, buf, inputPollInterval)
		if errors.Is(err, errPollTimeout) {
			continue
		}
		if err != nil || n == 0 { // EOF or a real read error ends the forwarder
			return
		}
		if _, werr := ptmx.Write(buf[:n]); werr != nil {
			return
		}
	}
}

// pollRead waits up to wait for fd to become readable, then reads once. It
// reports errPollTimeout when the wait elapses with no input and (0, nil) at
// EOF.
func pollRead(fd int, buf []byte, wait time.Duration) (int, error) {
	pfds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(pfds, int(wait.Milliseconds()))
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, errPollTimeout
		}
		break
	}
	return unix.Read(fd, buf)
}

// exitCode reports the agent's exit status, following the shell convention of
// 128+signal when it was killed rather than exiting on its own.
func exitCode(waitErr error) (int, error) {
	if waitErr == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if !errors.As(waitErr, &ee) {
		return 0, waitErr
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal()), nil
	}
	return ee.ExitCode(), nil
}
