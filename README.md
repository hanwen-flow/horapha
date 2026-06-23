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

### Locating the server

`checkpoint`/`restore` accept the same bazel *startup* flags as a normal
invocation (the flags before the bazel command, e.g. `--output_base`,
`--output_user_root`). Those flags determine which server you talk to.

`horapha` runs `bazel <startup flags> info output_base` to resolve the path, then
reads the server pid from `$output_base/server/server.pid.txt`.

### Checkpoint / restore

Images are written under `$output_base/criu/`. CRIU is invoked with
`--unprivileged` (rootless), `--tree`/`--tree-aware` to capture the whole server
tree, `--tcp-established` to preserve the gRPC command port, and
`--restore-detached` on restore so the revived tree outlives `horapha`.

### PID namespace + init (experimental)

Rootless CRIU is happiest when it owns the namespaces of the tree it dumps, and
reproducible PIDs make restore reliable. Setting `HORAPHA_NS=1` makes `horapha`
re-execute itself into fresh **user + PID + mount** namespaces (no root, no
setuid helpers).

Inside the namespace, the re-exec'd `horapha` becomes **PID 1 and runs a small
init** (`internal/nsrun/init.go`) rather than exec'ing bazel directly. The init:

1. mounts a private `/proc` so tools and CRIU see the namespaced process view;
2. forks bazel as a child in its own process group;
3. forwards SIGINT/SIGTERM/SIGHUP/SIGQUIT to it;
4. reaps orphans — bazel *daemonizes* its server, which reparents to PID 1, so
   init must wait on it rather than leave a zombie;
5. propagates the primary child's exit status as its own.

Exec'ing bazel directly as PID 1 (the previous approach) was wrong: the bazel
*client* would be PID 1, and when it exited the namespace — and the server —
would be torn down. The init decouples the server's lifetime from the client.

#### Why a *persistent* init is required for server reuse

The current `HORAPHA_NS` is **per-invocation** (model A): each build gets a fresh
namespace, and after the primary client exits the init SIGTERMs any lingering
daemons and exits too. That is enough to exercise checkpoint of a single build,
but it does not give you a warm server reused across builds.

A discovered kernel constraint shapes the next step: **an unprivileged process
cannot `setns()` into an existing PID namespace**, even after entering the
owning user namespace and becoming euid 0 (verified on this host —
`setns(CLONE_NEWPID)` returns `EPERM`). So you cannot create a warm server in a
namespace and later `nsenter` a new client into it without privilege.

The persistent design (model B, not yet built) is therefore: a long-lived init
daemon owns the namespace and the warm bazel server, and clients reach it over a
**unix socket**; the daemon forks each `bazel` command as *its own* child inside
the namespace. Checkpoint/restore then snapshot that daemon's tree.

## Requirements

- Linux with unprivileged user namespaces enabled
  (`sysctl kernel.unprivileged_userns_clone=1` on some distros).
- `criu` >= 3.18 on `$PATH` (4.x recommended for `--unprivileged`).
- `bazel` or `bazelisk` on `$PATH`.

## Configuration

| Env var          | Effect                                        |
|------------------|-----------------------------------------------|
| `HORAPHA_BAZEL`  | bazel binary to wrap (default: bazelisk/bazel)|
| `HORAPHA_CRIU`   | criu binary to use (default: `criu`)          |
| `HORAPHA_NS`     | run bazel inside a PID namespace (experimental)|
| `HORAPHA_DEBUG`  | mirror CRIU logs to stderr                    |

## Build

```
go build -o horapha .
```

## Status

This is a research experiment. Expect rough edges, especially around server
persistence inside namespaces and CRIU restoring open file descriptors into the
output tree.
