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

	"github.com/engflow/horapha/internal/bazel"
)

// ChildArg is the sentinel first argument that marks a re-exec into the
// namespaced child. It is deliberately unlikely to collide with bazel.
const ChildArg = "__horapha_ns_child__"

// Run re-executes horapha inside new user+PID+mount namespaces and, in the
// child, execs argv[0] with argv[1:]. Stdio is inherited. It returns when the
// command exits.
func Run(ctx context.Context, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("nsrun: empty argv")
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("nsrun: locate self: %w", err)
	}

	args := append([]string{ChildArg}, argv...)
	cmd := exec.CommandContext(ctx, self, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// New user namespace lets an unprivileged user create the PID and
		// mount namespaces below; map our uid/gid to root inside.
		Cloneflags: syscall.CLONE_NEWUSER |
			syscall.CLONE_NEWPID |
			syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{
			ContainerID: 0,
			HostID:      os.Getuid(),
			Size:        1,
		}},
		GidMappings: []syscall.SysProcIDMap{{
			ContainerID: 0,
			HostID:      os.Getgid(),
			Size:        1,
		}},
		GidMappingsEnableSetgroups: false,
	}
	// The child runs an init that already translated bazel's status into its
	// own exit code; surface that as a bazel.ExitError so main mirrors it.
	err = cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &bazel.ExitError{Code: ee.ExitCode(), Err: err}
	}
	return err
}

// Child is the entry point of the re-executed process. It runs as PID 1 of the
// new PID namespace and hands off to the init loop, which mounts a private
// /proc, forks argv[0] argv[1:], reaps orphans, and forwards signals. The
// returned int is the exit code to use for the process.
func Child(argv []string) (int, error) {
	if len(argv) == 0 {
		return 1, fmt.Errorf("nsrun child: empty argv")
	}
	return runInit(argv)
}

func lookPath(name string) (string, error) {
	return exec.LookPath(name)
}
