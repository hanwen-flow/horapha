package nsrun

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/engflow/horapha/internal/bazel"
	"github.com/engflow/horapha/internal/criu"
)

// commandWaiters lets the control server launch commands without racing the
// namespace init's Wait4(-1). Only init reaps children.
type commandWaiters struct {
	mu         sync.Mutex
	outputBase string
	pending    map[int]commandWaiter
}

type commandWaiter struct {
	done      chan syscall.WaitStatus
	started   time.Time
	operation string
}

func (w *commandWaiters) run(ctx context.Context, argv []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	done := make(chan syscall.WaitStatus, 1)

	// Keep registration atomic with delivery, even if the child exits before
	// Start returns. Do not call cmd.Wait: init delivers the reaped status.
	w.mu.Lock()
	started := time.Now()
	if err := cmd.Start(); err != nil {
		w.mu.Unlock()
		return err
	}
	w.pending[cmd.Process.Pid] = commandWaiter{done, started, criu.Operation(argv)}
	w.mu.Unlock()
	defer cmd.Process.Release()

	var status syscall.WaitStatus
	select {
	case status = <-done:
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		return ctx.Err()
	}
	if code := waitStatusToCode(status); code != 0 {
		return &bazel.ExitError{Code: code}
	}
	return nil
}

func (w *commandWaiters) reaped(pid int, status syscall.WaitStatus, usage syscall.Rusage) {
	w.mu.Lock()
	waiter, ok := w.pending[pid]
	delete(w.pending, pid)
	w.mu.Unlock()
	if ok {
		if waiter.operation != "" {
			logControl(w.outputBase, "%s", criu.Usage(waiter.operation, pid, waitStatusToCode(status), time.Since(waiter.started), usage))
		}
		waiter.done <- status
	}
}
