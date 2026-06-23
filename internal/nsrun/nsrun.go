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
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/engflow/horapha/internal/bazel"
)

// ChildArg is the sentinel first argument that marks a re-exec into the
// namespaced child. It is deliberately unlikely to collide with bazel.
const ChildArg = "__horapha_ns_child__"

// Run re-executes horapha inside new user+PID+mount namespaces. The re-exec'd
// child becomes PID 1 and runs an init (see init.go) that forks argv as the
// foreground command. Run returns the foreground command's exit code as soon as
// it finishes, deliberately LEAVING the init (and any daemonized bazel server)
// running in the background so the server — and its namespaces — persist for a
// later checkpoint. The namespace lives exactly as long as that init does.
func Run(ctx context.Context, outputBase string, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("nsrun: empty argv")
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
		// New user namespace lets an unprivileged user create the PID and mount
		// namespaces below; map our uid/gid to root inside so we hold the
		// capabilities needed to mount /proc and run criu (an identity mapping
		// yields an empty capability set). Running as uid 0 makes getpwuid()
		// resolve "~" to /root, which would break tilde paths in the server
		// (e.g. a disk cache under $HOME); WithCheckpointableFlags passes
		// -Duser.home=$HOME to the server JVM to compensate.
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
	if err := cmd.Start(); err != nil {
		pw.Close()
		return fmt.Errorf("nsrun: start: %w", err)
	}
	// Close our copy of the write end so the read below sees EOF if init dies
	// without reporting.
	pw.Close()
	// Release the child so the Go runtime does not wait/reap it: init persists.
	_ = cmd.Process.Release()

	var buf [1]byte
	n, _ := pr.Read(buf[:])
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
	if len(argv) == 0 {
		return 1, fmt.Errorf("nsrun child: empty argv")
	}
	return runInit(argv)
}

func lookPath(name string) (string, error) {
	return exec.LookPath(name)
}
