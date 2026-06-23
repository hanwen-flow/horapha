// Package bazel locates the bazel binary, discovers paths such as the
// output_base for an invocation, and forwards command lines to bazel.
package bazel

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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

// OutputBase returns the output_base for the server identified by the given
// startup flags, by asking bazel itself: `bazel <startup> info output_base`.
//
// This deliberately starts the server if it is not already running, which is
// also what we want before a checkpoint.
func OutputBase(ctx context.Context, startupFlags []string) (string, error) {
	args := append(append([]string{}, startupFlags...), "info", "output_base")
	cmd := exec.CommandContext(ctx, Binary(), args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("bazel info output_base: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
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
