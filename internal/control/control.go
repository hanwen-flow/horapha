// Package control defines the tiny line-based protocol horapha uses to ask the
// in-namespace init to perform checkpoint operations on its behalf.
//
// Why a socket at all: criu dump/restore need CAP_CHECKPOINT_RESTORE, which is
// only held *inside* the user namespace the server lives in. A host-side
// `horapha checkpoint` cannot get that capability and cannot re-enter the
// namespace (setns into a PID namespace is EPERM for the unprivileged; nsenter
// -U is EINVAL). So the init — already running as PID 1 inside the namespace —
// is the only thing that can run criu. horapha connects to its control socket
// and asks.
//
// This is NOT proxying bazel traffic: ordinary bazel clients still talk to the
// server directly over gRPC/TCP. The socket carries only checkpoint control.
//
// The socket lives on the shared output_base mount so the host can reach it.
package control

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"syscall"
)

// SockName is the control socket, relative to output_base.
const SockName = "horapha.sock"

// Commands (request is a single line; response is "OK[ message]" or "ERR
// message").
const (
	CmdPing = "PING"
	// CmdCheckpoint dumps the server and leaves it running.
	CmdCheckpoint = "CHECKPOINT"
	// CmdCheckpointStop attempts a dump, then tears down the owned namespace
	// even if capture fails. A failed attempt is not a usable checkpoint.
	CmdCheckpointStop = "CHECKPOINT_STOP"
)

// SockPath returns the control socket path for an output_base.
func SockPath(outputBase string) string {
	return filepath.Join(outputBase, SockName)
}

// PeerPID returns the init's PID as seen by the caller, using credentials
// supplied by the kernel rather than a namespace-local PID in the protocol.
func PeerPID(outputBase string) (int, error) {
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: SockPath(outputBase), Net: "unix"})
	if err != nil {
		return 0, fmt.Errorf("dial control socket: %w", err)
	}
	defer conn.Close()
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, fmt.Errorf("read control peer credentials: %w", credErr)
	}
	if cred.Pid <= 0 {
		return 0, fmt.Errorf("control peer has no visible PID")
	}
	return int(cred.Pid), nil
}

// Response is the parsed reply from the init.
type Response struct {
	OK      bool
	Message string
}

// Request dials the control socket, sends one command line, and returns the
// parsed response. The caller controls its deadline and cancellation.
func Request(ctx context.Context, outputBase, cmd string) (Response, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", SockPath(outputBase))
	if err != nil {
		return Response{}, fmt.Errorf("dial control socket (is the namespaced server running?): %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	if _, err := fmt.Fprintf(conn, "%s\n", cmd); err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, fmt.Errorf("read control response: %w", err)
	}
	return parseResponse(line), nil
}

func parseResponse(line string) Response {
	line = strings.TrimRight(line, "\r\n")
	if rest, ok := strings.CutPrefix(line, "OK"); ok {
		return Response{OK: true, Message: strings.TrimSpace(rest)}
	}
	if rest, ok := strings.CutPrefix(line, "ERR"); ok {
		return Response{OK: false, Message: strings.TrimSpace(rest)}
	}
	return Response{OK: false, Message: "malformed response: " + line}
}

// FormatOK / FormatErr build server-side response lines.
func FormatOK(msg string) string  { return "OK " + msg + "\n" }
func FormatErr(msg string) string { return "ERR " + msg + "\n" }
