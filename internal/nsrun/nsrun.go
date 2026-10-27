// Package nsrun runs a child command (bazel) inside a fresh PID namespace so
// that the resulting process tree has stable, low PIDs and can be checkpointed
// with rootless CRIU.
//
// Rootless CRIU needs the checkpointed tree to live in a user namespace it
// owns, and a PID namespace makes the captured PIDs reproducible on restore.
// We achieve both by re-executing horapha itself with a sentinel first
// argument: the parent (Run) sets up the namespaces via SysProcAttr, and the
// re-executed child (Child) becomes PID 1 and runs a small init that forks the
// real command, reaps orphans, and forwards signals (see init.go).
//
// Why an init at all? Bazel daemonizes its server, which then reparents to
// PID 1 of the namespace. If we exec'd bazel directly as PID 1, the client
// exiting would tear down the namespace and kill the server. A proper init
// lets the server's lifetime be managed independently of the client.
package nsrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/engflow/horapha/internal/bazel"
	"github.com/engflow/horapha/internal/criu"
)

// ChildArg is the sentinel first argument that marks a re-exec into the
// namespaced child. It is deliberately unlikely to collide with bazel.
const ChildArg = "__horapha_ns_child__"

const interruptGrace = 5 * time.Second

// Linux UAPI capability numbers (linux/capability.h).
const (
	capSysPtrace         = 19
	capSysAdmin          = 21
	capCheckpointRestore = 40
)

// Run re-executes horapha inside new user+PID+mount namespaces. The re-exec'd
// child becomes PID 1 and runs an init (see init.go) that forks argv as the
// foreground command. Run returns the foreground command's exit code as soon as
// it finishes, deliberately LEAVING the init (and any daemonized bazel server)
// running in the background so the server — and its namespaces — persist for a
// later checkpoint. If non-nil, finish runs after successful foreground exit
// while the init process is still owned. Canceled runs, failed CRIU restores,
// and finalization failures instead tear down their newly created namespace.
func Run(ctx context.Context, outputBase string, argv []string, finish func(initPID int) error) error {
	if len(argv) == 0 {
		return fmt.Errorf("nsrun: empty argv")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if os.Getuid() == 0 || os.Geteuid() != os.Getuid() || os.Getegid() != os.Getgid() {
		return fmt.Errorf("nsrun: requires a non-root user with matching real and effective IDs")
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("nsrun: locate self: %w", err)
	}

	// Status pipe: init writes the foreground exit code, then we return.
	pr, pw, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("nsrun: pipe: %w", err)
	}
	defer pr.Close()

	args := append([]string{ChildArg}, argv...)
	// Note: not CommandContext — we must NOT kill the child when Run returns;
	// the init is meant to outlive us.
	cmd := exec.Command(self, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.ExtraFiles = []*os.File{pw} // becomes fd 3 (statusFD) in the child
	// Tell the init where output_base is, so it can serve the control socket.
	cmd.Env = os.Environ()
	if outputBase != "" {
		cmd.Env = append(cmd.Env, OutputBaseEnv+"="+outputBase)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// Keep tools non-root: archive extractors otherwise try to preserve
		// unmapped owners. Init and its descendants retain these capabilities
		// only in the new user namespace, not in the host namespace.
		Cloneflags: syscall.CLONE_NEWUSER |
			syscall.CLONE_NEWPID |
			syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{
			ContainerID: os.Getuid(),
			HostID:      os.Getuid(),
			Size:        1,
		}},
		GidMappings: []syscall.SysProcIDMap{{
			ContainerID: os.Getgid(),
			HostID:      os.Getgid(),
			Size:        1,
		}},
		GidMappingsEnableSetgroups: false,
		AmbientCaps:                []uintptr{capSysAdmin, capSysPtrace, capCheckpointRestore},
	}
	if err := cmd.Start(); err != nil {
		pw.Close()
		return fmt.Errorf("nsrun: start: %w", err)
	}
	// Close our copy of the write end so the read below sees EOF if init dies
	// without reporting.
	pw.Close()
	// Keep the process handle until cancellation forwarding has stopped. A normal
	// Bazel client detaches; canceled runs and failed restores own teardown.
	defer cmd.Process.Release()
	err = waitClient(ctx, cmd.Process, pr)
	if err == nil && finish != nil {
		err = finish(cmd.Process.Pid)
	}
	canceled := ctx.Err()
	if canceled != nil {
		err = canceled
	}
	if canceled != nil || (err != nil && (finish != nil || criu.Operation(argv) == "restore")) {
		// Killing our namespace's PID 1 also terminates its descendants, including
		// detached restored tasks and codec children outside the foreground group.
		if killErr := cmd.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			return errors.Join(err, fmt.Errorf("nsrun: stop namespace init: %w", killErr))
		}
		if _, waitErr := cmd.Process.Wait(); waitErr != nil {
			return errors.Join(err, fmt.Errorf("nsrun: reap namespace init: %w", waitErr))
		}
	}
	return err
}

func waitClient(ctx context.Context, init *os.Process, status *os.File) error {
	finished := make(chan struct{})
	forwarded := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		// Init forwards SIGINT to the initial client's process group.
		_ = init.Signal(os.Interrupt)
		timer := time.NewTimer(interruptGrace)
		defer timer.Stop()
		select {
		case <-finished:
		case <-timer.C:
			_ = init.Kill()
		}
		close(forwarded)
	})
	defer func() {
		close(finished)
		if !stop() {
			<-forwarded
		}
	}()

	var buf [1]byte
	n, _ := status.Read(buf[:])
	if err := ctx.Err(); err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("nsrun: init exited before reporting status")
	}
	if code := int(buf[0]); code != 0 {
		return &bazel.ExitError{Code: code}
	}
	return nil
}

// Child is the entry point of the re-executed process. It runs as PID 1 of the
// new PID namespace and hands off to the init loop, which mounts a private
// /proc, forks argv[0] argv[1:], reaps orphans, and forwards signals. The
// returned int is the exit code to use for the process.
func Child(argv []string) (int, error) {
	restoreReexec := len(argv) > 0 && argv[0] == restoreReexecArg
	if restoreReexec {
		argv = argv[1:]
	}
	if len(argv) == 0 {
		return 1, fmt.Errorf("nsrun child: empty argv")
	}
	return runInit(argv, restoreReexec)
}

func lookPath(name string) (string, error) {
	return exec.LookPath(name)
}
