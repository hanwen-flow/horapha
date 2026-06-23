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
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"time"
)

// SockName is the control socket, relative to output_base.
const SockName = "horapha.sock"

// Commands (request is a single line; response is "OK[ message]" or "ERR
// message").
const (
	CmdPing       = "PING"
	CmdCheckpoint = "CHECKPOINT"
)

// SockPath returns the control socket path for an output_base.
func SockPath(outputBase string) string {
	return filepath.Join(outputBase, SockName)
}

// Response is the parsed reply from the init.
type Response struct {
	OK      bool
	Message string
}

// Request dials the control socket, sends one command line, and returns the
// parsed response. A zero timeout means no deadline.
func Request(outputBase, cmd string, timeout time.Duration) (Response, error) {
	conn, err := net.Dial("unix", SockPath(outputBase))
	if err != nil {
		return Response{}, fmt.Errorf("dial control socket (is the namespaced server running?): %w", err)
	}
	defer conn.Close()

	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}

	if _, err := fmt.Fprintf(conn, "%s\n", cmd); err != nil {
		return Response{}, err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
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
