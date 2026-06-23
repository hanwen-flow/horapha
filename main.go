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
		// Resolve output_base the way bazel itself would (explicit flag, else
		// derived from the workspace root and output_user_root), so the user
		// rarely needs --output_base. Empty only when there is no workspace and
		// no explicit base — in which case bazel could not resolve one either,
		// and we just forward verbatim.
		ob, _ := bazel.ResolveOutputBase(bazel.StartupFlags(args))
		if ob != "" {
			os.Setenv(bazel.JNIDirEnv, filepath.Join(ob, "horapha-jni"))
		}
		args = bazel.WithCheckpointableFlags(args)

		// Auto-restore: if no namespaced server is running but a checkpoint
		// exists, bring the warm server back before forwarding the command, so
		// the user transparently reattaches to their saved server instead of
		// cold-starting a new one. Best-effort — on failure we fall through to
		// a normal (cold) bazel invocation.
		if ob != "" && !namespacedServerRunning(ob) && checkpoint.Exists(ob) {
			fmt.Fprintf(os.Stderr, "horapha: no server running; restoring checkpoint\n")
			if err := checkpoint.Restore(ctx, []string{"--output_base=" + ob}); err != nil {
				fmt.Fprintf(os.Stderr, "horapha: auto-restore failed (%v); starting fresh\n", err)
			}
		}

		// By default horapha runs bazel inside a fresh PID namespace under an
		// init, so the server tree has reproducible PIDs for rootless CRIU —
		// that is the whole point of the wrapper. We only do this when an
		// explicit --output_base is given (we need a known location for the
		// control socket / JNI dir; without it we cannot manage the server) and
		// when no namespaced server is already running. Set HORAPHA_NO_NS=1 to
		// opt out and forward to bazel verbatim.
		//
		// If a namespaced server is already up, fall through to a plain host
		// client, which attaches over loopback via the rewritten rawproto;
		// re-entering would otherwise spawn a fresh namespace each time (its
		// client cannot see the existing server's host pid in its own /proc).
		if os.Getenv("HORAPHA_NO_NS") == "" && ob != "" && !namespacedServerRunning(ob) {
			argv := append([]string{bazel.Binary()}, args...)
			err := nsrun.Run(ctx, ob, argv)
			// The server now runs in the namespace and has written its
			// server_info.rawproto advertising its *namespace-local* pid, which
			// a host client cannot verify. Rewrite it to the host pid so plain
			// host clients can attach over loopback — without this they would
			// fail verification and start a competing server. Best-effort: a
			// failure here only costs the transparent-reattach optimization.
			makeServerReachable(ob)
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
