// Command horapha is an experimental wrapper around bazel.
//
// By default horapha forwards its entire command line to bazel unchanged. It
// additionally understands two commands that are *not* forwarded:
//
//	horapha checkpoint [bazel startup flags...]
//	horapha restore    [bazel startup flags...]
//
// These guess the output_base of the corresponding bazel invocation and run
// rootless CRIU to save to / restore from $output_base/criu/. To make the
// bazel server checkpointable, horapha can run it inside a PID namespace.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/engflow/horapha/internal/bazel"
	"github.com/engflow/horapha/internal/checkpoint"
	"github.com/engflow/horapha/internal/nsrun"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	// nsrun re-executes this binary inside a fresh PID/user namespace using a
	// hidden first argument. Handle that before anything else.
	if len(args) > 0 && args[0] == nsrun.ChildArg {
		code, err := nsrun.Child(args[1:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "horapha: child: %v\n", err)
		}
		return code
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch firstArg(args) {
	case "checkpoint":
		return toExit(checkpoint.Checkpoint(ctx, args[1:]))
	case "restore":
		return toExit(checkpoint.Restore(ctx, args[1:]))
	default:
		// Everything else is forwarded verbatim to bazel. When HORAPHA_NS is
		// set we run bazel inside a fresh PID namespace (experimental) so the
		// server tree has reproducible PIDs for rootless CRIU.
		if os.Getenv("HORAPHA_NS") != "" {
			argv := append([]string{bazel.Binary()}, args...)
			return toExit(nsrun.Run(ctx, argv))
		}
		return toExit(bazel.Passthrough(ctx, args))
	}
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// toExit maps an error to a process exit code, preserving bazel's own exit
// codes when the error carries one.
func toExit(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec_ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	fmt.Fprintf(os.Stderr, "horapha: %v\n", err)
	return 1
}

// exec_ExitError lets internal packages surface a specific exit code without
// importing os/exec into main.
type exec_ExitError = bazel.ExitError
