// Package checkpoint implements the horapha "checkpoint" and "restore"
// subcommands, which save/restore a running bazel server via rootless CRIU.
//
// The server is expected to have been started under `HORAPHA_NS=1` so it lives
// in a PID namespace: that keeps its namespace-local pid stable across a
// checkpoint/restore cycle (CRIU can re-create the exact pid), which is what
// makes restore reliable. Because the client validates the server by reading
// server_info.rawproto and looking the pid up in the host /proc, horapha
// rewrites that file to the server's *host* pid (see internal/serverinfo).
package checkpoint

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/engflow/horapha/internal/bazel"
	"github.com/engflow/horapha/internal/control"
	"github.com/engflow/horapha/internal/criu"
	"github.com/engflow/horapha/internal/nsrun"
	"github.com/engflow/horapha/internal/serverinfo"
)

// imagesSubdir is where checkpoint images live, relative to output_base.
const imagesSubdir = "criu"

// Checkpoint asks the in-namespace init (over its control socket) to dump the
// bazel server into $output_base/criu/.
//
// criu must run inside the namespace because CAP_CHECKPOINT_RESTORE is only
// held there and the host cannot re-enter the namespace; the init is our agent
// inside it. See internal/control.
func Checkpoint(ctx context.Context, startupArgs []string) error {
	startup := bazel.StartupFlags(startupArgs)

	// Resolve output_base WITHOUT running bazel. `bazel info` would fail to
	// attach to the namespaced server (its rawproto still advertises the
	// namespace-local pid) and start a *competing* host server, which
	// overwrites server.pid.txt and trips the namespaced server's
	// PidFileWatcher into halting. So checkpoint requires explicit
	// --output_base, same as restore.
	outputBase, ok := bazel.ExplicitOutputBase(startup)
	if !ok {
		return fmt.Errorf("checkpoint requires an explicit --output_base startup flag")
	}

	dir := filepath.Join(outputBase, imagesSubdir)
	fmt.Fprintf(os.Stderr, "horapha: requesting checkpoint -> %s\n", dir)

	resp, err := control.Request(outputBase, control.CmdCheckpoint, 5*time.Minute)
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("checkpoint failed: %s", resp.Message)
	}
	fmt.Fprintf(os.Stderr, "horapha: %s\n", resp.Message)
	return nil
}

// Restore brings the bazel server back from $output_base/criu/ and rewrites the
// server's on-disk identity so a host-side bazel client can attach to it.
func Restore(ctx context.Context, startupArgs []string) error {
	startup := bazel.StartupFlags(startupArgs)

	// Resolve output_base WITHOUT running bazel: `bazel info` would start a
	// fresh host server and clobber the checkpoint we are about to restore. We
	// therefore require an explicit --output_base for restore.
	outputBase, ok := bazel.ExplicitOutputBase(startup)
	if !ok {
		return fmt.Errorf("restore requires an explicit --output_base startup flag")
	}

	dir := filepath.Join(outputBase, imagesSubdir)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("no checkpoint at %s: %w", dir, err)
	}

	// The namespace-local pid was recorded into the images dir at checkpoint
	// time. We use it (not bazel's mutable server.pid.txt, which a competing
	// server could have overwritten) to find the restored server's host pid.
	nsPID, err := readCheckpointPID(dir)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "horapha: restoring bazel server (ns pid %d) from %s\n", nsPID, dir)

	// Run criu restore as the foreground command of a fresh namespace. The
	// init recreates the server (which criu reparents to init via
	// --restore-detached) and then persists, holding the namespace open and
	// serving the control socket — exactly as if the server had been launched
	// with HORAPHA_NS=1.
	restoreArgv := criu.RestoreArgs(options(dir))
	if err := nsrun.Run(ctx, outputBase, restoreArgv); err != nil {
		return fmt.Errorf("criu restore in namespace: %w", err)
	}

	// The restored server has a new host pid and a new start time. Point the
	// client's validation files at it. The restored tree is reparented
	// asynchronously, so wait for the java server (not a criu transient) to
	// appear under the checkpointed namespace pid.
	hostPID, err := serverinfo.WaitHostPIDForNSPID(nsPID, "java", 30*time.Second)
	if err != nil {
		return fmt.Errorf("locate restored server (ns pid %d): %w", nsPID, err)
	}
	if err := serverinfo.RewriteForHostPID(outputBase, hostPID); err != nil {
		return fmt.Errorf("rewrite server identity for host pid %d: %w", hostPID, err)
	}
	fmt.Fprintf(os.Stderr, "horapha: restored; server reachable at host pid %d\n", hostPID)
	return nil
}

// readCheckpointPID reads the namespace-local pid recorded at checkpoint time.
func readCheckpointPID(imagesDir string) (int, error) {
	b, err := os.ReadFile(filepath.Join(imagesDir, serverinfo.CheckpointPIDName))
	if err != nil {
		return 0, fmt.Errorf("read checkpoint pid (was a checkpoint taken?): %w", err)
	}
	pid, err := serverinfo.ParsePID(string(b))
	if err != nil {
		return 0, fmt.Errorf("parse checkpoint pid: %w", err)
	}
	return pid, nil
}

func options(dir string) criu.Options {
	return criu.Options{
		ImagesDir:    dir,
		ShellJob:     true,
		TCPClose:     true,
		Unprivileged: true,
		LogToStderr:  os.Getenv("HORAPHA_DEBUG") != "",
	}
}
