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
	"path/filepath"
	"syscall"
	"time"

	"github.com/engflow/horapha/internal/bazel"
	"github.com/engflow/horapha/internal/checkpoint"
	"github.com/engflow/horapha/internal/control"
	"github.com/engflow/horapha/internal/nsrun"
	"github.com/engflow/horapha/internal/serverinfo"
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
		// Everything else is forwarded to bazel. We always inject the
		// checkpointable startup flags (and JNI env), on EVERY invocation, not
		// just the one that starts the server: bazel refuses to attach to a
		// running server whose startup options differ from the client's. If
		// only the server-launching call carried these flags, ordinary
		// `horapha build`/`info` clients would mismatch and force a restart.
		// See internal/serverinfo and the JniLoader patch in the bazel tree.
		ob, _ := bazel.ExplicitOutputBase(bazel.StartupFlags(args))
		if ob != "" {
			os.Setenv(bazel.JNIDirEnv, filepath.Join(ob, "horapha-jni"))
		}
		args = bazel.WithCheckpointableFlags(args)

		// When HORAPHA_NS is set we run bazel inside a fresh PID namespace under
		// an init, so the server tree has reproducible PIDs for rootless CRIU.
		// But only START a namespaced server if one is not already running:
		// otherwise re-running `HORAPHA_NS=1 horapha ...` would spawn a fresh
		// namespace every time (its client cannot see the existing server's
		// host pid in its own /proc) and never reuse the warm server. If an
		// init is already serving the control socket, fall through to a plain
		// host client, which attaches over loopback via the rewritten rawproto.
		if os.Getenv("HORAPHA_NS") != "" && !namespacedServerRunning(ob) {
			argv := append([]string{bazel.Binary()}, args...)
			err := nsrun.Run(ctx, ob, argv)
			// The server now runs in the namespace and has written its
			// server_info.rawproto advertising its *namespace-local* pid, which
			// a host client cannot verify. Rewrite it to the host pid so plain
			// host clients can attach over loopback — without this they would
			// fail verification and start a competing server. Best-effort: a
			// failure here only costs the transparent-reattach optimization.
			if ob != "" {
				makeServerReachable(ob)
			}
			return toExit(err)
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

// namespacedServerRunning reports whether an in-namespace init is already
// serving the checkpoint control socket for this output_base, i.e. a warm
// namespaced server already exists and we should attach rather than start one.
func namespacedServerRunning(outputBase string) bool {
	if outputBase == "" {
		return false
	}
	resp, err := control.Request(outputBase, control.CmdPing, 2*time.Second)
	return err == nil && resp.OK
}

// makeServerReachable rewrites the namespaced server's identity files so a
// host-side bazel client can attach to it. Best-effort; errors are reported but
// not fatal (the build itself already succeeded).
func makeServerReachable(outputBase string) {
	nsPID, err := bazel.ServerPID(outputBase)
	if err != nil {
		fmt.Fprintf(os.Stderr, "horapha: could not read server pid: %v\n", err)
		return
	}
	if _, err := serverinfo.MakeReachable(nsPID, "java", outputBase, 10*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "horapha: could not make server reachable: %v\n", err)
	}
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
