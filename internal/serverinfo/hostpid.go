package serverinfo

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// WaitHostPIDForNSPID polls HostPIDForNSPID until it finds a process whose comm
// matches wantComm (e.g. "java" for the bazel server), or the timeout elapses.
// This is used after `criu restore --restore-detached`, where the restored tree
// is reparented asynchronously and a transient (criu itself, a forking helper)
// may briefly hold the namespace pid before the real server settles.
func WaitHostPIDForNSPID(ctx context.Context, nsPID int, namespace os.FileInfo, wantComm string, timeout time.Duration) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return 0, fmt.Errorf("waiting for restored server (ns pid %d): %w (last lookup: %v)", nsPID, err, lastErr)
		}
		hostPID, err := HostPIDForNSPID(nsPID, namespace)
		if err == nil {
			if wantComm == "" || comm(hostPID) == wantComm {
				return hostPID, nil
			}
			lastErr = fmt.Errorf("ns pid %d maps to host %d (comm %q, want %q)",
				nsPID, hostPID, comm(hostPID), wantComm)
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

func comm(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// HostPIDForNSPID scans /proc and returns the host pid of the process whose
// innermost namespace pid equals nsPID in the specified PID namespace.
//
// The NSpid line in /proc/<host>/status lists a process's pid in each pid
// namespace from outermost (host) to innermost; the last field is the pid as
// seen inside its own namespace.
func HostPIDForNSPID(nsPID int, namespace os.FileInfo) (int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		hostPID, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a pid dir
		}
		candidate, err := os.Stat(fmt.Sprintf("/proc/%d/ns/pid", hostPID))
		if err != nil || !os.SameFile(namespace, candidate) {
			continue
		}
		inner, ok := innerNSPID(hostPID)
		if !ok {
			continue
		}
		if inner == nsPID {
			return hostPID, nil
		}
	}
	return 0, fmt.Errorf("no host process maps to namespace pid %d", nsPID)
}

// innerNSPID returns the innermost-namespace pid for a host pid.
func innerNSPID(hostPID int) (int, bool) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/status", hostPID))
	if err != nil {
		return 0, false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		rest, ok := strings.CutPrefix(line, "NSpid:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0, false
		}
		inner, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil {
			return 0, false
		}
		return inner, true
	}
	return 0, false
}
