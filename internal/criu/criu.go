// Package criu is a thin wrapper around the criu(8) command line.
//
// criu dump/restore require CAP_CHECKPOINT_RESTORE. In horapha that capability
// is only held *inside* the user namespace the server lives in (an unprivileged
// host process does not have it and cannot re-enter the namespace). Therefore
// criu is always invoked from within the namespace by the init process, where
// we are effectively root over our own userns — so --unprivileged is usually
// unnecessary and left off by default.
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
	// ShellJob allows checkpointing processes that have a controlling terminal
	// / are not session leaders.
	ShellJob bool
	// TCPClose closes established TCP connections on dump and does not try to
	// restore them. The bazel server's clients are transient and reconnect, so
	// we close rather than preserve the gRPC command-port connections.
	TCPClose bool
	// LeaveRunning leaves the dumped tree running after a successful dump
	// (otherwise criu kills it). Only meaningful for Dump.
	LeaveRunning bool
	// Unprivileged relaxes criu's checks for rootless operation. Only needed
	// when criu lacks full capabilities; left off when run inside our userns.
	Unprivileged bool
	// LogToStderr mirrors criu logs to stderr.
	LogToStderr bool
	// Extra are additional raw arguments appended to every invocation.
	Extra []string
}

func (o Options) common() []string {
	args := []string{
		"--images-dir", o.ImagesDir,
		// Raise the ghost-file size limit so any large deleted-but-mapped files
		// criu does copy into the image are not rejected.
		"--ghost-limit", "1000000000",
		// Bazel writes files with restrictive modes; skip criu's rwx sanity
		// check that would otherwise refuse to dump them.
		"--skip-file-rwx-check",
	}
	if o.ShellJob {
		args = append(args, "--shell-job")
	}
	if o.TCPClose {
		args = append(args, "--tcp-close")
	}
	if o.Unprivileged {
		// Required for rootless dump/restore on the shared host network
		// namespace. criu touches privileged net state (e.g. netns links, and
		// SO_SNDBUFFORCE/SO_RCVBUFFORCE on restore) that returns EPERM for an
		// unprivileged user; --unprivileged makes criu treat those as
		// non-fatal. We keep the host netns (rather than an isolated one) so a
		// host bazel client can still reach the server's gRPC port over
		// loopback.
		args = append(args, "--unprivileged")
	}
	if o.LogToStderr {
		args = append(args, "--log-file", "/proc/self/fd/2")
	} else {
		// Always keep a log inside the images dir for diagnostics. criu
		// defaults to a relative path; make it explicit and absolute.
		args = append(args, "--log-file", o.ImagesDir+"/criu.log", "-v4")
	}
	return append(args, o.Extra...)
}

// Dump checkpoints the process tree rooted at pid into opts.ImagesDir.
func Dump(ctx context.Context, pid int, opts Options) error {
	if err := os.MkdirAll(opts.ImagesDir, 0o755); err != nil {
		return err
	}
	args := []string{"dump", "--tree", strconv.Itoa(pid)}
	if opts.LeaveRunning {
		args = append(args, "--leave-running")
	}
	args = append(args, opts.common()...)
	return runCRIU(ctx, args)
}

// RestoreArgs returns the full criu argv (including the binary) for a restore.
// Restore runs inside the namespace as another process's foreground command
// (the namespace's init adopts the --restore-detached tree), so horapha needs
// the argv rather than running criu directly here.
func RestoreArgs(opts Options) []string {
	return append([]string{Binary(), "restore", "--restore-detached"}, opts.common()...)
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
