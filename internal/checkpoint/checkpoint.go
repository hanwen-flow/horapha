// Package checkpoint implements the horapha "checkpoint" and "restore"
// subcommands, which save/restore a running bazel server via rootless CRIU.
package checkpoint

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/engflow/horapha/internal/bazel"
	"github.com/engflow/horapha/internal/criu"
)

// imagesDir is where checkpoint images live, relative to output_base.
const imagesSubdir = "criu"

// Checkpoint locates the bazel server for the given startup flags and dumps it
// into $output_base/criu/.
func Checkpoint(ctx context.Context, startupArgs []string) error {
	startup := bazel.StartupFlags(startupArgs)

	outputBase, err := bazel.OutputBase(ctx, startup)
	if err != nil {
		return err
	}
	pid, err := bazel.ServerPID(outputBase)
	if err != nil {
		return fmt.Errorf("read server pid (is the bazel server running?): %w", err)
	}

	dir := filepath.Join(outputBase, imagesSubdir)
	fmt.Fprintf(os.Stderr, "horapha: checkpointing bazel server pid %d -> %s\n", pid, dir)

	return criu.Dump(ctx, pid, options(dir))
}

// Restore brings the bazel server back from $output_base/criu/.
func Restore(ctx context.Context, startupArgs []string) error {
	startup := bazel.StartupFlags(startupArgs)

	// We need output_base without starting a server. Prefer asking bazel, but
	// if a server is already up that would defeat the purpose; the caller is
	// expected to run restore when no server is running. bazel info still
	// resolves the path layout deterministically from startup flags.
	outputBase, err := bazel.OutputBase(ctx, startup)
	if err != nil {
		return err
	}

	dir := filepath.Join(outputBase, imagesSubdir)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("no checkpoint at %s: %w", dir, err)
	}
	fmt.Fprintf(os.Stderr, "horapha: restoring bazel server from %s\n", dir)

	return criu.Restore(ctx, options(dir))
}

func options(dir string) criu.Options {
	return criu.Options{
		ImagesDir:      dir,
		ShellJob:       true,
		TCPEstablished: true,
		LogToStderr:    os.Getenv("HORAPHA_DEBUG") != "",
	}
}
