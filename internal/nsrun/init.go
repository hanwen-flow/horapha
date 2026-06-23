package nsrun

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// runInit is the PID 1 of the new PID namespace. A real init must do three
// things that a plain exec cannot:
//
//  1. Reap orphans. Any process whose parent dies reparents to PID 1; if PID 1
//     never wait()s, they pile up as zombies. Crucially, bazel *daemonizes* its
//     server, which makes the server reparent to us — so we must outlive and
//     reap on its behalf rather than exec'ing bazel directly.
//  2. Handle signals. The kernel installs no default dispositions for PID 1, so
//     SIGTERM/SIGINT do nothing unless we catch them. We forward them to the
//     primary child.
//  3. Propagate the primary child's exit status as our own.
//
// All child management is done with raw ForkExec/Wait4 (not os/exec) so that
// the Go runtime's own SIGCHLD reaper does not race us on wait4(-1, ...).
func runInit(argv []string) (exitCode int, err error) {
	if err := mountProc(); err != nil {
		return 1, err
	}

	bin, err := lookPath(argv[0])
	if err != nil {
		return 1, fmt.Errorf("init: lookup %q: %w", argv[0], err)
	}

	// Forward a broad set of signals to the primary child's process group.
	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh,
		syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)

	primary, err := syscall.ForkExec(bin, argv, &syscall.ProcAttr{
		Env:   os.Environ(),
		Files: []uintptr{0, 1, 2},
		Sys: &syscall.SysProcAttr{
			// Put the child in its own process group so we can signal the whole
			// group, and so it is not in PID 1's group (which would route
			// terminal signals oddly).
			Setpgid: true,
		},
	})
	if err != nil {
		return 1, fmt.Errorf("init: start %q: %w", argv[0], err)
	}

	go func() {
		for sig := range sigCh {
			// Forward to the primary's process group (negative pid).
			_ = syscall.Kill(-primary, sig.(syscall.Signal))
		}
	}()

	// Reaper loop: block in wait4(-1) reaping every child. When the primary is
	// reaped, remember its status but keep reaping until no children remain, so
	// we leave no zombies behind.
	primaryCode := 0
	primaryDone := false
	for {
		var ws syscall.WaitStatus
		wpid, werr := syscall.Wait4(-1, &ws, 0, nil)
		if werr == syscall.EINTR {
			continue
		}
		if werr == syscall.ECHILD {
			break // no children left
		}
		if werr != nil {
			return 1, fmt.Errorf("init: wait4: %w", werr)
		}
		if wpid == primary {
			primaryDone = true
			primaryCode = waitStatusToCode(ws)
			// Ask any lingering daemons in the namespace to shut down, then
			// keep looping to reap them. Without this, a daemonized bazel
			// server would keep us alive forever (model A is per-invocation).
			_ = signalAllExceptSelf(syscall.SIGTERM)
		}
		_ = primaryDone
	}

	signal.Stop(sigCh)
	close(sigCh)
	return primaryCode, nil
}

func mountProc() error {
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("init: make-rprivate: %w", err)
	}
	if err := syscall.Mount("proc", "/proc", "proc",
		syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, ""); err != nil {
		return fmt.Errorf("init: mount /proc: %w", err)
	}
	return nil
}

func waitStatusToCode(ws syscall.WaitStatus) int {
	switch {
	case ws.Exited():
		return ws.ExitStatus()
	case ws.Signaled():
		return 128 + int(ws.Signal())
	default:
		return 1
	}
}

// signalAllExceptSelf sends sig to every process in the PID namespace except
// PID 1 (us). kill(-1, sig) does exactly that.
func signalAllExceptSelf(sig syscall.Signal) error {
	return syscall.Kill(-1, sig)
}
