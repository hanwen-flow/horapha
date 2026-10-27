package nsrun

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/engflow/horapha/internal/control"
	"github.com/engflow/horapha/internal/criu"
	"github.com/engflow/horapha/internal/serverinfo"
)

// serveControl runs the init's control socket. It lets a host-side horapha ask
// us (PID 1 inside the namespace, where CAP_CHECKPOINT_RESTORE is held) to run
// criu against the server. outputBase is passed via $HORAPHA_OUTPUT_BASE so the
// init knows where the socket, pid file, and images dir live.
//
// Runs until ctx is cancelled; errors are logged to stderr but never fatal —
// losing the control socket must not take down the server.
func serveControl(ctx context.Context, outputBase string, commands *commandWaiters) {
	// output_base may not exist yet: bazel creates it as it starts, and the
	// control server races that. Create it so we can bind immediately.
	if err := os.MkdirAll(outputBase, 0o755); err != nil {
		logControl(outputBase, "mkdir output_base: %v", err)
		return
	}

	sock := control.SockPath(outputBase)
	// Stale socket from a previous run would block bind.
	_ = os.Remove(sock)

	ln, err := net.Listen("unix", sock)
	if err != nil {
		logControl(outputBase, "listen: %v", err)
		return
	}
	logControl(outputBase, "listening on %s", sock)
	go func() {
		<-ctx.Done()
		_ = ln.Close()
		_ = os.Remove(sock)
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed
		}
		if handleControlConn(ctx, conn, outputBase, commands) {
			// The reply is closed before PID 1 tears down its owned namespace.
			_ = syscall.Kill(-1, syscall.SIGKILL)
			return
		}
	}
}

// handleControlConn reports whether the namespace must stop after the reply.
func handleControlConn(ctx context.Context, conn net.Conn, outputBase string, commands *commandWaiters) bool {
	defer conn.Close()
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return false
	}
	cmd := strings.TrimSpace(line)

	switch cmd {
	case control.CmdPing:
		_, _ = conn.Write([]byte(control.FormatOK("pong")))
	case control.CmdCheckpoint:
		if err := doCheckpoint(ctx, outputBase, true, commands); err != nil {
			_, _ = conn.Write([]byte(control.FormatErr(err.Error())))
			return false
		}
		_, _ = conn.Write([]byte(control.FormatOK("checkpointed")))
	case control.CmdCheckpointStop:
		ctx, cancel := context.WithCancel(ctx)
		disconnected := make(chan struct{})
		go func() {
			_, _ = io.Copy(io.Discard, conn)
			cancel()
			close(disconnected)
		}()
		defer func() {
			conn.Close()
			cancel()
			<-disconnected
		}()
		// Stop even on failure: a codec or metadata error can occur after CRIU
		// has successfully dumped the tree and left it stopped.
		if err := doCheckpoint(ctx, outputBase, false, commands); err != nil {
			_, _ = conn.Write([]byte(control.FormatErr(err.Error())))
			return true
		}
		_, _ = conn.Write([]byte(control.FormatOK("checkpointed and stopped")))
		return true
	default:
		_, _ = conn.Write([]byte(control.FormatErr("unknown command: " + cmd)))
	}
	return false
}

// logControl appends a diagnostic line to $output_base/horapha-init.log, since
// the persisted init has detached its stderr to /dev/null.
func logControl(outputBase, format string, args ...any) {
	if outputBase == "" {
		return
	}
	_ = os.MkdirAll(outputBase, 0o755)
	f, err := os.OpenFile(filepath.Join(outputBase, "horapha-init.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "horapha-init: "+format+"\n", args...)
}

// doCheckpoint runs criu dump against the bazel server, from inside the
// namespace, by the server's namespace-local pid (read from server.pid.txt).
// When leaveRunning is true the server keeps serving after the dump; when false
// it stays stopped until the caller tears down the namespace.
func doCheckpoint(ctx context.Context, outputBase string, leaveRunning bool, commands *commandWaiters) error {
	pidBytes, err := os.ReadFile(filepath.Join(outputBase, "server", "server.pid.txt"))
	if err != nil {
		return fmt.Errorf("read server pid: %w", err)
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(pidBytes)), "%d", &pid); err != nil {
		return fmt.Errorf("parse server pid: %w", err)
	}

	imagesDir := filepath.Join(outputBase, "criu")
	if err := os.MkdirAll(imagesDir, 0o755); err != nil {
		return err
	}
	boundFile := filepath.Join(imagesDir, checkpointPIDBoundName)
	if err := os.Remove(boundFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove previous checkpoint PID bound: %w", err)
	}
	argv := criu.DumpArgs(pid, criu.Options{
		ImagesDir:    imagesDir,
		ShellJob:     true,
		TCPClose:     true,
		LeaveRunning: leaveRunning,
		Unprivileged: true,
	})
	err = commands.run(ctx, argv)
	if err != nil {
		logControl(outputBase, "criu dump failed: %v (see %s/dump.log)", err, imagesDir)
		return err
	}
	// Record the checkpointed namespace-local pid alongside the images. restore
	// needs it to locate the restored server, and must not rely on bazel's
	// mutable server.pid.txt (which a competing server could overwrite).
	if werr := os.WriteFile(filepath.Join(imagesDir, serverinfo.CheckpointPIDName),
		fmt.Appendf(nil, "%d\n", pid), 0o644); werr != nil {
		return fmt.Errorf("record checkpoint pid: %w", werr)
	}
	if !leaveRunning {
		// Only a stopped tree still has exactly the threads saved by CRIU.
		// Leave-running checkpoints remain supported, but cannot be restored
		// by the guarded prototype without a fresh checkpoint --stop.
		bound, err := namespacePIDBound("/proc", pid)
		if err != nil {
			return fmt.Errorf("record stopped checkpoint PID bound: %w", err)
		}
		if err := writePIDBound(boundFile, bound); err != nil {
			return err
		}
	}
	return nil
}
