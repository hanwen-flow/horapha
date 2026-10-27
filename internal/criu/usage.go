package criu

import (
	"fmt"
	"syscall"
	"time"
)

// Operation identifies only commands launched through the configured CRIU binary.
func Operation(argv []string) string {
	if len(argv) >= 2 && argv[0] == Binary() && (argv[1] == "dump" || argv[1] == "restore") {
		return argv[1]
	}
	return ""
}

// Usage reports wait4 counters, including children already reaped by the command.
// Surviving restored processes and external stream providers are not included.
// If the configured binary is a wrapper, its completed work is included too.
func Usage(operation string, pid, exitCode int, elapsed time.Duration, r syscall.Rusage) string {
	return fmt.Sprintf("CRIU_RUSAGE {\"operation\":%q,\"scope\":\"waited_command_and_reaped_children\",\"namespace_pid\":%d,\"exit_code\":%d,\"wall_seconds\":%.6f,\"user_seconds\":%.6f,\"system_seconds\":%.6f,\"minor_faults\":%d,\"major_faults\":%d,\"voluntary_context_switches\":%d,\"involuntary_context_switches\":%d,\"max_rss_kib\":%d}",
		operation, pid, exitCode, elapsed.Seconds(),
		float64(r.Utime.Nano())/float64(time.Second), float64(r.Stime.Nano())/float64(time.Second),
		r.Minflt, r.Majflt, r.Nvcsw, r.Nivcsw, r.Maxrss)
}
