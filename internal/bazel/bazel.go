// Package bazel locates the bazel binary, discovers paths such as the
// output_base for an invocation, and forwards command lines to bazel.
package bazel

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// ExitError carries a child process exit code so callers can mirror bazel's
// exit status.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("exited with code %d", e.Code)
}

// Binary returns the bazel executable to use. It honours $HORAPHA_BAZEL, then
// falls back to "bazelisk" and finally "bazel" on PATH.
func Binary() string {
	if b := os.Getenv("HORAPHA_BAZEL"); b != "" {
		return b
	}
	for _, cand := range []string{"bazelisk", "bazel"} {
		if p, err := exec.LookPath(cand); err == nil {
			return p
		}
	}
	return "bazel"
}

// Passthrough runs bazel with the given arguments, inheriting stdio, and
// returns an *ExitError carrying bazel's exit code on failure.
func Passthrough(ctx context.Context, args []string) error {
	cmd := exec.CommandContext(ctx, Binary(), args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return wrapExit(cmd.Run())
}

// JNIDirEnv is read by the (patched) bazel server's JniLoader: when set, JNI
// libraries are extracted to that directory and left on disk instead of being
// unlinked, so rootless CRIU can dump their file-backed mappings.
const JNIDirEnv = "HORAPHA_JNI_DIR"

// nettyNoDeleteFlag stops Netty (used by bazel's gRPC stack) from deleting its
// extracted native library after loading, for the same CRIU reason as
// JNIDirEnv. It is a bazel startup flag (affects the server JVM).
const nettyNoDeleteFlag = "--host_jvm_args=-Dio.netty.native.deleteLibAfterLoading=false"

// WithCheckpointableFlags inserts the bazel startup flags required to make the
// server checkpointable, ahead of the user's arguments. Startup flags must
// precede the bazel command, so they go at the front. Each flag is added only
// if an equivalent is not already present.
func WithCheckpointableFlags(args []string) []string {
	// The server runs as uid 0 inside the user namespace, so the JVM's
	// user.home would otherwise resolve to /root via getpwuid; pin it to the
	// real home so bazel expands "~" (e.g. in a disk cache path) correctly.
	userHomeFlag := "--host_jvm_args=-Duser.home=" + homeDir()

	var prepend []string
	if !slices.Contains(args, nettyNoDeleteFlag) {
		prepend = append(prepend, nettyNoDeleteFlag)
	}
	if !hasHostJVMArg(args, "-Duser.home=") {
		prepend = append(prepend, userHomeFlag)
	}
	return append(prepend, args...)
}

// hasHostJVMArg reports whether args already contains a --host_jvm_args flag
// carrying the given JVM property prefix (so we do not override a user's
// explicit value).
func hasHostJVMArg(args []string, jvmPrefix string) bool {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--host_jvm_args="); ok && strings.HasPrefix(v, jvmPrefix) {
			return true
		}
	}
	return false
}

// StartupFlags extracts the bazel *startup* flags from a horapha argument list.
// Startup flags are those appearing before the bazel command (e.g. "build").
// checkpoint/restore accept the same startup flags so they can locate the same
// server (output_base depends on --output_base/--output_user_root etc.).
func StartupFlags(args []string) []string {
	var flags []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			continue
		}
		break
	}
	return flags
}

// ExplicitOutputBase returns the value of an explicit --output_base startup
// flag (in either "--output_base=X" or "--output_base X" form), and whether it
// was present. Unlike OutputBase it never launches bazel, which matters for
// restore: running `bazel info` there would start a fresh host server and
// clobber the checkpoint we are about to restore.
//
// The value is tilde- and env-expanded to match the path bazel itself uses:
// the shell does not expand "~" in a "--output_base=~/x" argument, but bazel
// does, so horapha must too — otherwise it looks for server files under a
// literal "~" directory.
func ExplicitOutputBase(startupFlags []string) (string, bool) {
	const key = "--output_base"
	for i, a := range startupFlags {
		if v, ok := strings.CutPrefix(a, key+"="); ok {
			return expandPath(v), true
		}
		if a == key && i+1 < len(startupFlags) {
			return expandPath(startupFlags[i+1]), true
		}
	}
	return "", false
}

// expandPath resolves a leading "~" or "~/" to the user's home directory, the
// way bazel expands output_base paths.
func expandPath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + strings.TrimPrefix(p, "~")
		}
	}
	return p
}

// ServerPID reads the bazel server pid from $output_base/server/server.pid.txt.
func ServerPID(outputBase string) (int, error) {
	return readPIDFile(outputBase + "/server/server.pid.txt")
}

func wrapExit(err error) error {
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	if asExit(err, &ee) {
		return &ExitError{Code: ee.ExitCode(), Err: err}
	}
	return err
}
