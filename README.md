# horapha

`horapha` is an **experimental** wrapper around [Bazel](https://bazel.build).

By default it forwards its entire command line to `bazel` unchanged:

```
horapha build //...
horapha test //foo:bar
horapha --output_base=/tmp/ob info
```

On top of that, it adds two commands that are *not* forwarded to bazel:

```
horapha checkpoint [bazel startup flags...]
horapha restore    [bazel startup flags...]
```

These guess the `output_base` of the corresponding bazel invocation and run
**rootless** [CRIU](https://criu.org) to save to / restore from
`$output_base/criu/`.

## Why

A warm bazel server holds an expensive in-memory analysis cache (the skyframe
graph, loaded packages, the JIT-warmed JVM). Restarting bazel — or evicting the
server — throws that away. The goal of this experiment is to snapshot a warm
server to disk and bring it back later, e.g. across machine reboots, CI runners,
or to ship a pre-warmed cache.

## How it works

### Running the server in a namespace (default)

By default (whenever an `output_base` can be resolved — see below), `horapha`
re-executes itself into fresh **user + PID + mount** namespaces (no root, no
setuid helpers) and becomes **PID 1 running a small init**
(`internal/nsrun/init.go`), which then forks bazel. Set `HORAPHA_NO_NS=1` to
disable this and forward to bazel verbatim. The init:

1. mounts a private `/proc` so tools and CRIU see the namespaced process view;
2. forks bazel as the foreground command, forwards signals, and reaps orphans —
   bazel *daemonizes* its server, which reparents to PID 1;
3. reports the foreground command's exit code back to the launching `horapha`
   over a pipe, then **keeps running detached**, holding the namespace (and the
   warm server) open for a later checkpoint;
4. serves a small checkpoint **control socket** at `$output_base/horapha.sock`.

The PID namespace gives the server a stable, low namespace-local pid that CRIU
re-creates exactly on restore. The network namespace is **shared with the host**
so an ordinary `bazel` client can still reach the server's gRPC port over
loopback (see below).

### Checkpoint / restore

`checkpoint`/`restore` take the same bazel *startup* flags as a normal
invocation. Images live under `$output_base/criu/`.

### Resolving the output_base

horapha replicates bazel's own algorithm to find the `output_base`, so you
rarely need to pass `--output_base`:

- an explicit `--output_base` startup flag wins (with `~` expanded);
- otherwise it is `<output_user_root>/<md5(workspace_root)>`, where the
  workspace root is the nearest ancestor of the cwd containing a
  `MODULE.bazel`/`REPO.bazel`/`WORKSPACE.bazel`/`WORKSPACE` file, and
  `output_user_root` defaults to `${XDG_CACHE_HOME:-$HOME/.cache}/bazel/_bazel_$USER`
  (or an explicit `--output_user_root`).

This matches `bazel info output_base` exactly. horapha never runs `bazel info`
to discover it (that would start a competing server); it computes the path
itself. If you are not inside a workspace and give no `--output_base`, horapha
cannot resolve one — but neither could bazel — and just forwards verbatim.

CRIU needs `CAP_CHECKPOINT_RESTORE`, which is only held *inside* the user
namespace — and an unprivileged host process cannot re-enter a PID namespace
(`setns(CLONE_NEWPID)` is `EPERM`; `nsenter -U` is `EINVAL`). So **CRIU runs
inside the namespace, driven by the init**:

- `horapha checkpoint` connects to the control socket and asks the init to run
  `criu dump --leave-running`. The checkpointed namespace-local pid is recorded
  in `criu/ns-pid`. Pass `--stop` to dump and then tear the server down
  (`horapha checkpoint --stop`), e.g. to free resources or to test restore.
- `horapha restore` launches `criu restore` as the foreground command of a fresh
  namespace; the init adopts the restored server and persists.
- **Auto-restore:** an ordinary `horapha` command (e.g. `build`) with no running
  server but an existing `criu/` checkpoint restores it automatically before
  forwarding, so you transparently land on your warm server.

CRIU is run with `--unprivileged`, `--tcp-close` (clients reconnect),
`--ghost-limit`, and `--skip-file-rwx-check`. We keep the **host network
namespace** rather than isolating it, so the restored server's gRPC port stays
reachable from host clients; `--unprivileged` makes CRIU treat the resulting
privileged-net operations as non-fatal.

### How a host client reattaches to the namespaced server

The bazel client decides whether to attach by reading
`$output_base/server/server_info.rawproto` **from disk** (not over the wire) and
verifying the `pid` field against the host `/proc` plus `server.starttime`. A
server in a PID namespace records its *namespace-local* pid there, which a host
client would resolve to the wrong process. So horapha rewrites the `pid` field of
`server_info.rawproto` to the server's **host** pid (and, on restore, refreshes
`server.starttime`). It deliberately leaves `server.pid.txt` alone, because the
server's `PidFileWatcher` hard-exits if that file stops matching its own
namespace-local pid. See `internal/serverinfo`.

### Required bazel patch

The bazel server extracts its JNI libraries (`libunix_jni.so`, and Netty's
native lib) to `/tmp` and **unlinks them while keeping them mapped**. Rootless
CRIU cannot dump such deleted mappings (reading `/proc/PID/map_files/<addr>`
needs `CAP_SYS_ADMIN` in the *initial* user namespace, which a rootless userns
never has). horapha therefore needs a small patch to bazel's `JniLoader`
(honouring `HORAPHA_JNI_DIR`: extract to a stable dir and skip the unlink), and
sets `-Dio.netty.native.deleteLibAfterLoading=false` for Netty. With both, the
libraries stay file-backed and CRIU dumps them normally.

## Requirements

- Linux with unprivileged user namespaces enabled
  (`sysctl kernel.unprivileged_userns_clone=1` on some distros).
- `criu` >= 3.18 on `$PATH` (4.x recommended for `--unprivileged`).
- A bazel built with the `JniLoader` patch above (point `HORAPHA_BAZEL` at it).

## Configuration

| Env var           | Effect                                                 |
|-------------------|--------------------------------------------------------|
| `HORAPHA_BAZEL`   | bazel binary to wrap (default: bazelisk/bazel)         |
| `HORAPHA_CRIU`    | criu binary to use (default: `criu`)                   |
| `HORAPHA_NO_NS`   | disable namespace mode; forward to bazel verbatim      |
| `HORAPHA_JNI_DIR` | set automatically: where the patched bazel keeps JNI libs |
| `HORAPHA_DEBUG`   | mirror CRIU logs to stderr                             |

## Build

Build `horapha` itself:

```
go build -o horapha .
```

### Building the patched bazel

horapha requires a bazel built with the `JniLoader` patch (see "Required bazel
patch" above). Build the patched binary from a bazel checkout that carries the
patch:

```
cd /path/to/bazel
bazel build //src:bazel-dev
# → bazel-bin/src/bazel-dev
```

The `bazel-dev` binary is compiled for a recent JDK (e.g. Java 21), but the
server defaults to bazel's embedded JDK (often 11), which fails with
`UnsupportedClassVersionError`. Run the server on a matching JDK with
`--server_javabase`. The simplest way to apply both that flag and the patched
binary is a small wrapper that `HORAPHA_BAZEL` points at:

```sh
cat > ~/bin/bazel-horapha <<'EOF'
#!/bin/bash
exec /path/to/bazel/bazel-bin/src/bazel-dev \
  --server_javabase=/usr/lib/jvm/java-21-openjdk-amd64 "$@"
EOF
chmod +x ~/bin/bazel-horapha
```

Then point horapha at it:

```sh
export HORAPHA_BAZEL=~/bin/bazel-horapha
cd ~/my/workspace        # a dir under a MODULE.bazel / WORKSPACE

# namespace mode is the default; this starts a checkpointable server.
# output_base is derived like bazel does, so no --output_base needed.
horapha build //...

# snapshot it (the server keeps running; add --stop to tear it down too)
horapha checkpoint

# ...later (even after the processes are gone), bring it back
horapha restore

# ordinary clients attach to the warm server transparently — and if the
# server is gone but a checkpoint exists, this auto-restores it first
horapha build //...
```

horapha injects the JNI flags automatically on every invocation, so plain
clients share the server's startup fingerprint and attach instead of starting
their own. Pass `--output_base=…` (or run outside a workspace) to override the
derived location.

## Status

This is a research experiment. The full cycle works end-to-end: a warm
namespaced server can be checkpointed, the machine state thrown away, restored
(automatically, on the next command), and a stock bazel client reattaches to the
revived server. Rough edges remain (multi-server and cleanup edge cases are not
hardened).
