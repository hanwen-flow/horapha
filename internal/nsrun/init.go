package nsrun

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// OutputBaseEnv carries output_base from Run into the re-exec'd init, so the
// init can place its control socket and find the server pid / images dir.
const OutputBaseEnv = "HORAPHA_OUTPUT_BASE"

// statusFD is the inherited pipe (ExtraFiles[0] => fd 3 in the child) on which
// init reports the foreground command's exit code, as a single byte, so the
// parent can return to the shell while init keeps running.
const statusFD = 3

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
//  3. Propagate the primary child's exit status (over the status pipe).
//
// After the foreground command exits, init does NOT kill the (daemonized) bazel
// server: it reports the exit code up the status pipe, redirects its own stdio
// to /dev/null, and keeps reaping until the namespace is empty. This keeps the
// server — and the PID/user namespaces it lives in — alive after horapha
// returns to the shell, which is what makes a later `horapha checkpoint`
// possible. The namespace persists exactly as long as this init does.
//
// All child management uses raw ForkExec/Wait4 (not os/exec) so the Go runtime
// SIGCHLD reaper does not race us on wait4(-1, ...).
func runInit(argv []string) (exitCode int, err error) {
	if err := mountProc(); err != nil {
		return 1, err
	}

	bin, err := lookPath(argv[0])
	if err != nil {
		return 1, fmt.Errorf("init: lookup %q: %w", argv[0], err)
	}

	// Serve the checkpoint control socket for the life of the namespace.
	ctlCtx, ctlCancel := context.WithCancel(context.Background())
	defer ctlCancel()
	ob := os.Getenv(OutputBaseEnv)
	logControl(ob, "init entry: output_base=%q pid=%d", ob, os.Getpid())
	if ob != "" {
		go serveControl(ctlCtx, ob)
	}

	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh,
		syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)

	primary, err := syscall.ForkExec(bin, argv, &syscall.ProcAttr{
		Env:   os.Environ(),
		Files: []uintptr{0, 1, 2},
		Sys: &syscall.SysProcAttr{
			// Own process group so we can signal the whole foreground group.
			Setpgid: true,
		},
	})
	if err != nil {
		return 1, fmt.Errorf("init: start %q: %w", argv[0], err)
	}

	go func() {
		for sig := range sigCh {
			_ = syscall.Kill(-primary, sig.(syscall.Signal))
		}
	}()

	// Reaper loop. Record the foreground command's status when it is reaped,
	// report it to the parent, then keep reaping any remaining processes (the
	// persisted server) until the namespace empties.
	primaryCode := 0
	reported := false
	for {
		var ws syscall.WaitStatus
		wpid, werr := syscall.Wait4(-1, &ws, 0, nil)
		if werr == syscall.EINTR {
			continue
		}
		if werr == syscall.ECHILD {
			break // namespace is empty; nothing left to hold open
		}
		if werr != nil {
			return 1, fmt.Errorf("init: wait4: %w", werr)
		}
		if wpid == primary && !reported {
			primaryCode = waitStatusToCode(ws)
			reportStatus(primaryCode)
			reported = true
			// Detach from the controlling terminal's stdio so the parent's
			// shell sees EOF and we can run quietly in the background.
			detachStdio()
		}
	}

	signal.Stop(sigCh)
	close(sigCh)
	if !reported {
		reportStatus(primaryCode)
	}
	return primaryCode, nil
}

// reportStatus writes the exit code byte to the status pipe and closes it,
// signaling the parent it may return. Best-effort: if the fd is absent (e.g.
// init run without a parent pipe) this is a no-op.
func reportStatus(code int) {
	f := os.NewFile(uintptr(statusFD), "status")
	if f == nil {
		return
	}
	_, _ = f.Write([]byte{byte(code)})
	_ = f.Close()
}

// detachStdio redirects fds 0/1/2 to /dev/null so the persisted init no longer
// holds the parent's terminal open.
func detachStdio() {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return
	}
	fd := int(null.Fd())
	_ = syscall.Dup2(fd, 0)
	_ = syscall.Dup2(fd, 1)
	_ = syscall.Dup2(fd, 2)
	if fd > 2 {
		_ = null.Close()
	}
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
