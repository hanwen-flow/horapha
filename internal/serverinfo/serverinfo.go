// Package serverinfo manipulates the small on-disk files the bazel client uses
// to find and validate a running server, so that a server living inside a PID
// namespace can be reached by an ordinary host-side bazel client.
//
// Background (verified against bazel's source):
//
//   - The client reads $output_base/server/server_info.rawproto from disk and
//     trusts its `pid` field (proto field 1, a varint). It does NOT learn the
//     pid over the gRPC wire — PingResponse carries only a cookie.
//   - Before connecting, the client calls VerifyServerProcess(pid): it reads
//     /proc/<pid>/stat field 22 (start time in jiffies) and compares it to
//     $output_base/server/server.starttime.
//   - A server started inside a PID namespace records its *namespace-local* pid
//     (e.g. 11) in both rawproto and server.pid.txt. A host client resolves
//     that pid in the host's /proc, finds the wrong/no process, the start time
//     mismatches, and it starts a fresh server instead of attaching.
//
// The fix is to rewrite rawproto's `pid` field to the server's *host* pid. The
// start time stored in server.starttime is a kernel boot-relative value that is
// identical regardless of which namespace you read it from, so for a live
// checkpoint it already matches the host pid and needs no change. After a CRIU
// restore the process has a new host pid AND a new start time, so both must be
// rewritten — see RewriteForHostPID.
//
// We deliberately do NOT touch server.pid.txt: bazel's PidFileWatcher polls it
// and hard-halts the server (Runtime.halt, no shutdown hooks) if its contents
// stop matching the server's own in-memory (namespace-local) pid.
package serverinfo

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Files within $output_base/server.
const (
	RawprotoName  = "server_info.rawproto"
	StartTimeName = "server.starttime"
	PidFileName   = "server.pid.txt"
)

// CheckpointPIDName is the file (inside the criu images dir) where horapha
// records the checkpointed namespace-local pid, so restore can find the
// restored server without trusting bazel's mutable server.pid.txt.
const CheckpointPIDName = "ns-pid"

// ServerDir returns $output_base/server.
func ServerDir(outputBase string) string {
	return filepath.Join(outputBase, "server")
}

// RewriteForHostPID makes the server identified by hostPID reachable by a
// host-side bazel client, by writing hostPID into rawproto's pid field and
// hostPID's current start time into server.starttime.
//
// Use this after a restore (new pid, new start time). For a freshly
// checkpointed live server the start time is unchanged; rewriting it to the
// same value is harmless.
func RewriteForHostPID(outputBase string, hostPID int) error {
	dir := ServerDir(outputBase)

	start, err := ReadStartTime(hostPID)
	if err != nil {
		return fmt.Errorf("read start time of host pid %d: %w", hostPID, err)
	}
	if err := PatchRawprotoPID(filepath.Join(dir, RawprotoName), hostPID); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, StartTimeName), []byte(start), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", StartTimeName, err)
	}
	return nil
}

// PatchRawprotoPID rewrites field 1 (pid, a varint) of the ServerInfo proto at
// path to newPID, leaving every other field byte-for-byte intact.
func PatchRawprotoPID(path string, newPID int) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) == 0 || data[0] != 0x08 {
		// 0x08 == (field 1 << 3) | wiretype 0 (varint). ServerInfo declares pid
		// as field 1, and protobuf serializes fields in order, so it is first.
		return fmt.Errorf("%s: unexpected leading byte %#x, not a ServerInfo pid field", path, leadByte(data))
	}
	// Skip the existing varint value.
	i := 1
	for i < len(data) {
		b := data[i]
		i++
		if b&0x80 == 0 {
			break
		}
	}
	if i > len(data) {
		return fmt.Errorf("%s: truncated pid varint", path)
	}
	rest := data[i:]

	var out []byte
	out = append(out, 0x08)
	out = appendUvarint(out, uint64(newPID))
	out = append(out, rest...)

	// Write atomically so a reader never sees a half-written proto.
	return writeFileAtomic(path, out, 0o644)
}

// ReadStartTime returns field 22 (starttime, jiffies since boot) of
// /proc/<pid>/stat as a decimal string, matching what bazel writes to
// server.starttime.
func ReadStartTime(pid int) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	// The comm field (2nd) is parenthesized and may contain spaces; everything
	// after the closing ')' is space-separated. Field 22 (1-based) is index 19
	// in the post-')' slice (fields 3..N).
	line := string(b)
	rparen := strings.LastIndexByte(line, ')')
	if rparen < 0 {
		return "", fmt.Errorf("/proc/%d/stat: no comm terminator", pid)
	}
	fields := strings.Fields(line[rparen+1:])
	// After ')' the next field is #3 (state). starttime is #22, i.e. index 19.
	const startTimeIndexAfterComm = 19
	if len(fields) <= startTimeIndexAfterComm {
		return "", fmt.Errorf("/proc/%d/stat: too few fields (%d)", pid, len(fields))
	}
	return fields[startTimeIndexAfterComm], nil
}

func appendUvarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func leadByte(data []byte) byte {
	if len(data) == 0 {
		return 0
	}
	return data[0]
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".horapha.tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ParsePID is a small convenience for reading server.pid.txt-style files.
func ParsePID(s string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(s))
}
