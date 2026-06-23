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

### PID namespace (experimental)

Rootless CRIU is happiest when it owns the namespaces of the tree it dumps, and
reproducible PIDs make restore reliable. Setting `HORAPHA_NS=1` makes `horapha`
re-execute itself into fresh **user + PID + mount** namespaces (no root, no
setuid helpers) and mount a private `/proc` before exec'ing bazel.

> **Caveat — not yet seamless.** Bazel *daemonizes* its server. Run naively
> inside a PID namespace, the bazel *client* is PID 1; when it exits the
> namespace is torn down and the server dies with it. Making the server persist
> requires keeping a PID-1 reaper alive in the namespace for the server to be
> reparented to (and re-entering that namespace on restore). That plumbing is
> the open part of this experiment; today `HORAPHA_NS` only demonstrates the
> namespace setup.

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
