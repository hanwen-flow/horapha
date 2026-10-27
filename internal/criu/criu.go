// Package criu is a thin wrapper around the criu(8) command line.
//
// The init invokes CRIU inside the server's user namespace. Its capabilities
// apply only there; --unprivileged handles operations on shared host resources
// such as networking, where those capabilities confer no privilege.
package criu

import (
	"os"
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
	// LeaveRunning resumes the dumped tree after a successful dump;
	// otherwise it stays stopped. Only meaningful for Dump.
	LeaveRunning bool
	// Unprivileged relaxes CRIU's checks for rootless operation on shared resources.
	Unprivileged bool
	// LogToStderr mirrors criu logs to stderr.
	LogToStderr bool
	// Extra are additional raw arguments appended to every invocation.
	Extra []string
}

func (o Options) common() []string {
	args := []string{
		"--images-dir", o.ImagesDir,
		// Bazel holds persistent locks. The runner must exclusively own its
		// output and install directories across checkpoint and restore.
		"--file-locks",
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

// DumpArgs returns the full criu argv for a dump. The namespace init owns
// child reaping, so it must launch this command and collect its exit status.
func DumpArgs(pid int, opts Options) []string {
	args := []string{"dump", "--tree", strconv.Itoa(pid)}
	if opts.LeaveRunning {
		args = append(args, "--leave-running")
	} else {
		args = append(args, "--leave-stopped")
	}
	args = append(args, opts.common()...)
	return append([]string{Binary()}, args...)
}

// RestoreArgs returns the full criu argv (including the binary) for a restore.
// Restore runs inside the namespace as another process's foreground command
// (the namespace's init adopts the --restore-detached tree), so horapha needs
// the argv rather than running criu directly here.
func RestoreArgs(opts Options) []string {
	return append([]string{Binary(), "restore", "--restore-detached"}, opts.common()...)
}
