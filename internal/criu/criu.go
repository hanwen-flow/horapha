// Package criu is a thin wrapper around the criu(8) command line, configured
// for rootless (unprivileged) checkpoint/restore.
package criu

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// Binary is the criu executable. Override with $HORAPHA_CRIU.
func Binary() string {
	if b := os.Getenv("HORAPHA_CRIU"); b != "" {
		return b
	}
	return "criu"
}

// Options are common knobs for a dump/restore.
type Options struct {
	// ImagesDir is the directory holding the checkpoint images.
	ImagesDir string
	// ShellJob allows checkpointing processes that have a controlling
	// terminal / are not session leaders (the common case for a server we
	// started ourselves). Bazel servers are daemonised, but we keep this on
	// for robustness.
	ShellJob bool
	// TCPEstablished keeps established TCP connections (the gRPC command port)
	// across checkpoint/restore.
	TCPEstablished bool
	// LogToStderr mirrors criu logs to stderr in addition to the log file.
	LogToStderr bool
	// Extra are additional raw arguments appended to every invocation.
	Extra []string
}

func (o Options) common() []string {
	args := []string{
		"--images-dir", o.ImagesDir,
		// Rootless: CRIU 3.18+/4.x can dump & restore a tree owned by the
		// calling (non-root) user when it created the namespaces itself.
		"--unprivileged",
		// The bazel server forks worker processes / uses threads; capture the
		// whole tree.
		"--tree-aware",
	}
	if o.ShellJob {
		args = append(args, "--shell-job")
	}
	if o.TCPEstablished {
		args = append(args, "--tcp-established")
	}
	if o.LogToStderr {
		args = append(args, "--log-file", "/proc/self/fd/2")
	}
	return append(args, o.Extra...)
}

// Dump checkpoints the process tree rooted at pid into opts.ImagesDir. The
// target process is left running unless --leave-stopped is added via Extra; by
// default criu kills the dumped tree, which for a bazel server is acceptable
// because Restore brings it back.
func Dump(ctx context.Context, pid int, opts Options) error {
	if err := os.MkdirAll(opts.ImagesDir, 0o755); err != nil {
		return err
	}
	args := append([]string{"dump", "--tree", strconv.Itoa(pid)}, opts.common()...)
	return runCRIU(ctx, args)
}

// Restore restores a previously dumped tree from opts.ImagesDir. With
// --restore-detached the restored tree is reparented away from criu so it
// survives horapha exiting.
func Restore(ctx context.Context, opts Options) error {
	args := append([]string{"restore", "--restore-detached"}, opts.common()...)
	return runCRIU(ctx, args)
}

func runCRIU(ctx context.Context, args []string) error {
	cmd := exec.CommandContext(ctx, Binary(), args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("criu %s: %w", args[0], err)
	}
	return nil
}
