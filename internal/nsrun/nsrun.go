// Package nsrun runs a child command (bazel) inside a fresh PID namespace so
// that the resulting process tree has stable, low PIDs and can be checkpointed
// with rootless CRIU.
//
// Rootless CRIU needs the checkpointed tree to live in a user namespace it
// owns, and a PID namespace makes the captured PIDs reproducible on restore.
// We achieve both by re-executing horapha itself with a sentinel first
// argument: the parent sets up the namespaces via SysProcAttr, and the
// re-executed child (Child) mounts a private /proc and then execs the real
// command.
package nsrun

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
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
	return cmd.Run()
}

// Child is the entry point of the re-executed process. It runs as PID 1 of the
// new PID namespace, mounts a private /proc so that tools (and CRIU) see the
// namespaced view, then execs the real command argv[0] argv[1:].
func Child(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("nsrun child: empty argv")
	}

	// Make mount changes private to this namespace, then mount a fresh /proc.
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("nsrun child: make-rprivate: %w", err)
	}
	if err := syscall.Mount("proc", "/proc", "proc",
		syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, ""); err != nil {
		return fmt.Errorf("nsrun child: mount /proc: %w", err)
	}

	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("nsrun child: lookup %q: %w", argv[0], err)
	}
	// Replace ourselves with the target so it becomes PID 1 of the namespace.
	return syscall.Exec(bin, argv, os.Environ())
}
