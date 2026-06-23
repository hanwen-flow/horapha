package nsrun

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

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
func serveControl(ctx context.Context, outputBase string) {
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
		handleControlConn(ctx, conn, outputBase)
	}
}

func handleControlConn(ctx context.Context, conn net.Conn, outputBase string) {
	defer conn.Close()
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return
	}
	cmd := strings.TrimSpace(line)

	switch cmd {
	case control.CmdPing:
		_, _ = conn.Write([]byte(control.FormatOK("pong")))
	case control.CmdCheckpoint:
		if err := doCheckpoint(ctx, outputBase); err != nil {
			_, _ = conn.Write([]byte(control.FormatErr(err.Error())))
			return
		}
		_, _ = conn.Write([]byte(control.FormatOK("checkpointed")))
	default:
		_, _ = conn.Write([]byte(control.FormatErr("unknown command: " + cmd)))
	}
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
// namespace. We dump by the server's namespace-local pid (read from
// server.pid.txt) and --leave-running so the server keeps serving.
func doCheckpoint(ctx context.Context, outputBase string) error {
	pidBytes, err := os.ReadFile(filepath.Join(outputBase, "server", "server.pid.txt"))
	if err != nil {
		return fmt.Errorf("read server pid: %w", err)
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(pidBytes)), "%d", &pid); err != nil {
		return fmt.Errorf("parse server pid: %w", err)
	}

	imagesDir := filepath.Join(outputBase, "criu")
	err = criu.Dump(ctx, pid, criu.Options{
		ImagesDir:    imagesDir,
		ShellJob:     true,
		TCPClose:     true,
		LeaveRunning: true,
		Unprivileged: true,
	})
	if err != nil {
		logControl(outputBase, "criu dump failed: %v (see %s/dump.log)", err, imagesDir)
		return err
	}
	// Record the checkpointed namespace-local pid alongside the images. restore
	// needs it to locate the restored server, and must not rely on bazel's
	// mutable server.pid.txt (which a competing server could overwrite).
	if werr := os.WriteFile(filepath.Join(imagesDir, serverinfo.CheckpointPIDName),
		fmt.Appendf(nil, "%d\n", pid), 0o644); werr != nil {
		logControl(outputBase, "warn: record checkpoint pid: %v", werr)
	}
	return nil
}
