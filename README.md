# slinit

**A service manager and init system for Linux, written in Go.** Runs as
PID 1 or as an unprivileged per-user service manager, with a
dinit-compatible configuration format.

| | |
|---|---|
| **Latest release** | v3.0.1 — see [CHANGELOG.md](CHANGELOG.md) |
| **Compatibility contract** | [STABILITY.md](STABILITY.md) — control protocol v7 (min-compat v1) |
| **Requires** | Go 1.25+ to build; Linux to run |
| **License** | [Apache 2.0](LICENSE) |

The core is a port of [dinit](https://github.com/davmac314/dinit), with
features layered in from [runit](http://smarden.org/runit/),
[s6-linux-init](https://skarnet.org/software/s6-linux-init/),
[OpenRC](https://github.com/OpenRC/openrc),
[upstart](https://code.launchpad.net/upstart),
[finit](https://github.com/troglobit/finit),
sysvinit and the service-manager
subset of [systemd](https://systemd.io/). Runlevels, where present, are
UX aliases over the dependency graph — slinit does not carry a second
state machine or config format to accommodate them.

**Deliberately out of scope:** systemd's unit object model on D-Bus, and
the ecosystem daemons (networkd, resolved, homed). What *is* implemented
from that side is the service manager, a `journalctl` at 65/65 flag
parity with its own journal format, and `slinit-logind` for session and
seat management.

> **New here, or coming from systemd?** Start with the
> [operator's guide](doc/operators-guide.md): a systemctl ↔ slinitctl
> cheat-sheet, the handful of differences that change what you type,
> your first service, and troubleshooting.

## Install

```bash
go build ./...          # the daemon, slinitctl and 43 companion tools
```

Three binaries matter most:

| Binary | Role |
|---|---|
| `slinit` | the daemon — PID 1, system manager, or user manager |
| `slinitctl` | the control CLI (around 90 verbs, aliases included) |
| `slinit-runner` | post-fork execve wrapper; **required** by any service using LSM, seccomp or `restrict-*` |

The other 42 are linters, converters from other init systems, drop-in
clones of OpenRC and systemd utilities, the journal pipeline and the
container helpers — see [doc/tools.md](doc/tools.md). Every binary has a
man page under [doc/man](doc/man).

Optional compatibility symlinks:

```bash
ln -s slinit-shutdown slinit-reboot        # also slinit-halt, slinit-soft-reboot
ln -s slinit /sbin/halt                    # slinit dispatches on argv[0]
ln -s slinit /sbin/poweroff
ln -s slinit /sbin/reboot
```

## Quick start

```bash
# User mode (default) — no privileges needed
slinit --services-dir ~/.config/slinit.d

# System mode
slinit --system --services-dir /etc/slinit.d

# As PID 1, starting several boot services
slinit -t network -t web-server -t database

# Container mode (Docker/LXC/Podman): SIGINT/SIGTERM become a graceful halt
slinit --container -t myapp
```

Then, from another terminal:

```bash
slinitctl list                 # what is loaded, and in what state
slinitctl start myservice
slinitctl status myservice
```

Default service directories when `--services-dir` is not given:

* **system**: `/etc/slinit.d`, `/run/slinit.d`, `/usr/local/lib/slinit.d`, `/lib/slinit.d`
* **user**: `$XDG_CONFIG_HOME/slinit.d` (or `~/.config/slinit.d`), `/etc/slinit.d/user`, `/usr/lib/slinit.d/user`, `/usr/local/lib/slinit.d/user`

The flags above are the common ones. [slinit(8)](doc/man/slinit.8.md)
documents all 46; [doc/configuration.md](doc/configuration.md) keeps a
table of them for convenience.

## What it does

| Area | What is there |
|---|---|
| **Service types** | `process`, `scripted`, `bgprocess`, `internal`, `triggered` |
| **Dependencies** | 6 kinds — `depends-on`, `waits-for`, `depends-ms`, `before`, `after`, `prepared-by`; two-phase transitions; cycle and depth checks |
| **Lifecycle** | SIGTERM with per-service timeout then SIGKILL, restart policies with rate limiting and smooth recovery, `normal-exit` codes, pause/continue, pinning |
| **Activation** | explicit, dependency-driven, socket activation (`LISTEN_FDS`, Unix/TCP/UDP, `SO_REUSEPORT`), path activation via inotify, calendar timers, on-demand |
| **Readiness** | `pipefd`/`pipevar` protocol, `ready-check-command` polling, D-Bus name readiness, `sd_notify` |
| **Isolation & hardening** | filesystem sandbox (`private-tmp`, `protect-system`, bind/read-only/inaccessible paths), seccomp with curated syscall groups, the `restrict-*` and `protect-*` clusters, AppArmor/SELinux/SMACK transitions, capabilities, securebits — all fail-closed |
| **Resources** | cgroup v2 limits and weights, slices and delegation, nice, ioprio, oom-score-adj, rlimits, CPU affinity, PSI pressure watches |
| **Logging** | in-memory buffers, files with rotation/filtering/processors, consumer pipes, catch-all early-boot logger, a full journal pipeline (`slinit-journalctl` at 65/65 flag parity, FSS-sealed binary format, namespaces, message catalog) |
| **Control** | binary protocol over a Unix socket, goroutine per connection, push notifications for state and environment changes, runtime env and dependency edits, Prometheus metrics endpoint |
| **PID 1 duties** | console setup, Ctrl+Alt+Del, child subreaper and orphan reaping, four degrees of shutdown haste, soft-reboot, kexec, `switch-root`, hardware watchdog, UPS power events, boot-failure recovery with an interactive rescue prompt |

Roughly 250 systemd directives, all 12 upstart stanzas, and the runit,
s6, OpenRC, finit and sysvinit feature sets are covered;
[doc/features.md](doc/features.md) is the generated list of every name
the parser and the control protocol accept — grouped by upstream where
the provenance has been traced, which is about half of them so far.
[doc/roadmap.md](doc/roadmap.md) tracks what is planned.

### Where each upstream shows up

| Upstream | What slinit takes from it |
|---|---|
| **dinit** | the base: description format, dependency types, state machine, `slinitctl` verbs — 1:1. Adds `prepared-by` |
| **runit** | `finish-command`, `ready-check-command`, `pre-stop-hook`, `env-dir`, `control-command-<SIG>`, `chroot`, `new-session`, `lock-file`, log rotation/filtering, down-file, `once` |
| **s6-linux-init** | catch-all logger, TAI64N/ISO timestamps, scheduled shutdown and cancel, wall messages, `/etc/shutdown.allow`, global boot rlimits, container exit codes and ready-fd, `slinit-init-maker` |
| **OpenRC** | `rc-service` / `rc-update` / `rc-status` shims, `/etc/rc.conf` + `/etc/conf.d/<svc>` sourcing, named-runlevel dispatch, init.d/LSB auto-detection, drop-in clones of eight utilities |
| **upstart** | `manual`, `normal-exit`, `reload-signal`, `umask`, `author`/`version`/`usage`, AppArmor stanzas, `debug`, `script … end script`, `start-on-path-*`, `.override` drop-ins |
| **finit** | `switch-root`, watchdog-driven reboot, `tty-path = @console`, `slinit.cond=` boot modes, `/etc/rc.local` runparts, `/etc/network/interfaces`, `slinit-getty`, `slinit-watchdogd` |
| **sysvinit** | `SIGPWR` power events, `slinit-killall5`, `slinit-fstab-decode`, `/etc/inittab` conversion |
| **systemd** | the service-manager subset — start predicates, managed service directories, the sandbox/seccomp/protect clusters, credentials, timers, `journalctl` parity, `logind` |

Two things are deferred by design rather than missing: finit's
`org.finit` D-Bus control API (slinit stays Unix-socket-first) and a
dlopen-style plugin ABI (Go's build model makes a stable C ABI
expensive; `hooks.d/*` scripts and env-generator binaries cover the same
ground).

## Service configuration

Service files are `key = value`, dinit-compatible. Four shapes cover
most of what people write:

```ini
# /etc/slinit.d/myservice — a long-running process
type = process
command = /usr/bin/myservice --config /etc/myservice.conf
stop-command = /usr/bin/myservice --stop
stop-timeout = 10
restart = on-failure
restart-delay = 2
restart-limit-count = 3
restart-limit-interval = 60
depends-on: network
waits-for: logging
log-type = buffer
log-buffer-size = 4096
```

```ini
# /etc/slinit.d/mydaemon — a daemon that forks and writes a pidfile
type = bgprocess
command = /usr/sbin/mydaemon
pid-file = /run/mydaemon.pid
stop-timeout = 15
depends-on: network
```

```ini
# /etc/slinit.d/myapp — output to a file instead of a buffer
type = process
command = /usr/bin/myapp
log-type = file
logfile = /var/log/myapp.log
logfile-permissions = 0640
logfile-uid = 1000
logfile-gid = 1000
```

```ini
# /etc/slinit.d/worker — process attributes, limits and confinement
type = process
command = /usr/bin/worker
run-as = worker:worker
nice = 10
oom-score-adj = 500
ioprio = be:4
cpu-affinity = 0-3
rlimit-nofile = 1024:4096
cgroup = /sys/fs/cgroup/workers
capabilities = cap_net_bind_service,cap_sys_nice
securebits = noroot keep-caps
options = no-new-privs
env-file = /etc/worker.env
```

Templates (`name@argument` with `$1`), consumer pipes, socket and path
activation, shared loggers, console services, credentials and the rest
are in [doc/configuration.md](doc/configuration.md), with every
directive in [slinit-service(5)](doc/man/slinit-service.5.md).

`slinit-check <service>` validates a file offline — executables, paths,
dependencies — and `--online` checks it against the running daemon.

## slinitctl

```bash
# State
slinitctl list                          # every loaded service and its state
slinitctl status myservice              # one service, in detail
slinitctl is-started myservice          # exit code only, for scripts
slinitctl graph                         # dependency graph, Graphviz DOT

# Lifecycle
slinitctl start|stop|restart myservice
slinitctl wake|release myservice         # activate/deactivate without pinning
slinitctl once myservice                 # start without auto-restart
slinitctl signal HUP myservice
slinitctl pause|continue myservice       # SIGSTOP / SIGCONT
slinitctl run -- /usr/bin/thing          # transient service (systemd-run analogue)

# Configuration, at runtime
slinitctl reload myservice               # re-read from disk, no restart
slinitctl reload-all
slinitctl unload myservice                # drop a stopped service from memory
slinitctl enable|disable myservice        # add/remove a waits-for edge, persisted
slinitctl add-dep|rm-dep myservice waits-for other   # either argument order
slinitctl setenv KEY=VALUE                # also unsetenv, getallenv, reset-env

# Logs and attach
slinitctl catlog myservice                # the in-memory buffer
slinitctl attach myservice                # the service's virtual TTY, Ctrl+] detaches

# System
slinitctl shutdown poweroff|reboot|halt|kexec|soft-reboot
slinitctl shutdown reboot +5             # or HH:MM; `shutdown -c` cancels
slinitctl analyze                         # boot timing; `analyze plot` draws an SVG
slinitctl boot-time
```

Every subcommand, its flags and its exit codes:
[slinitctl(8)](doc/man/slinitctl.8.md). `--system` and `--user` pick the
instance explicitly.

## Architecture

slinit keeps dinit's service-management design and expresses it in Go:

* **goroutines and channels** instead of dinit's dasynq event loop
* **interfaces and struct embedding** instead of C++ virtual dispatch
* **two-phase state transitions** (propagation, then execution) — the
  correctness property inherited from dinit
* **one goroutine per child**, with channel notification on exit
* **one scheduling lock** held across every transition; a transition
  that cannot finish is reported with a goroutine dump to
  `/run/slinit-stall.stack` rather than hanging silently
* **binary control protocol** over a Unix socket, goroutine per
  connection, with push notifications for state and environment changes

```
cmd/              45 directories: 44 Go binaries + a shell resource agent
pkg/service/      state machine, service types, the dependency graph
pkg/config/       dinit-compatible parser and the service loader
pkg/control/      the binary protocol, server and connections
pkg/process/      fork/exec, child monitoring, fd handling
pkg/seccomp/      cBPF compiler, syscall groups, the restrict-* cluster
pkg/journal/      journal readers, writers and the binary format
pkg/shutdown/     shutdown sequences, power events, soft-reboot
doc/man/          46 man pages — the reference for every binary
tests/            unit, functional (QEMU), acceptance and container suites
```

### PID 1 signal handling

| Signal | Action | Sent by |
|---|---|---|
| `SIGTERM` | reboot | busybox `reboot` |
| `SIGINT` | reboot | Ctrl+Alt+Del |
| `SIGQUIT` | poweroff | — |
| `SIGUSR1` | reopen the control socket | recovery once the fs is writable |
| `SIGUSR2` | poweroff | busybox `poweroff` |
| `SIGPWR` | run the power hook | UPS daemons (nut, apcupsd) |
| `SIGHUP` | ignored | — |
| `SIGCHLD` | reap orphans | child exit |
| `SIGRTMIN+3…+6` | halt, poweroff, reboot, kexec | systemd-compatible containers |

The RT signals let `kill -s RTMIN+4 1` shut a container down cleanly
with no slinitctl in the image. Signal-driven shutdown can be gated by
`/etc/slinit/shutdown.allow`; the gate applies to the first trigger
only, so a second Ctrl+Alt+Del always escalates.

## Documentation

| | |
|---|---|
| [operator's guide](doc/operators-guide.md) | start here if you run systems: systemd mappings, first service, troubleshooting |
| [doc/configuration.md](doc/configuration.md) | service examples by shape, directive tables, daemon flags |
| [doc/tools.md](doc/tools.md) | the 43 companion binaries and how they are used |
| [doc/features.md](doc/features.md) | generated list of every accepted directive and opcode, by upstream |
| [doc/roadmap.md](doc/roadmap.md) | what is planned, and what has shipped |
| [doc/man/](doc/man) | 46 man pages — one per binary, plus slinit-service(5) |
| [STABILITY.md](STABILITY.md) | what will not break, and how deprecation works |
| [CHANGELOG.md](CHANGELOG.md) | release history, with the reasoning behind each version number |
| [CONTRIBUTING.md](CONTRIBUTING.md) | how to build, test and send a change |
| [SECURITY.md](SECURITY.md) | reporting a vulnerability |

## Testing

```bash
go test ./...                             # ~2400 unit tests and benchmarks
./tests/functional/run-tests.sh           # 230 cases, each in its own QEMU VM
./tests/functional/selftest.sh            # checks the harness can still fail a case
./tests/container/run.sh                  # 23 cases with slinit as a container's PID 1
go test -fuzz=FuzzConfigParse ./tests/fuzz
```

The suites that need a live target take their host over SSH:

```bash
ACCEPTANCE_HOST=... ACCEPTANCE_PORT=... ACCEPTANCE_USER=root \
  ./tests/acceptance/ssh/run.sh           # 219 cases
ACCEPTANCE_HOST=... ACCEPTANCE_PORT=... ACCEPTANCE_USER=root \
  ./tests/performance/ssh/run.sh          # 92 cases; see tests/performance/README.md
```

42 fuzz targets in all: 27 under `tests/fuzz`, the rest beside the code
they exercise, so `./tests/fuzz` alone does not reach them —
`grep -rl '^func Fuzz' --include='*_test.go' .` finds every one. CI runs
the unit, functional, fuzz and performance suites plus a nightly
container soak; the SSH, container and k8s tiers run downstream against
real targets.

A functional run reports passes, skips and failures separately: around
one case in ten skips for want of cgroup v2, `chrt`, a machine-id, a TPM
or NUMA in the VM. Read all three numbers.

## Contributing

[CONTRIBUTING.md](CONTRIBUTING.md) has the build, test and review
expectations. The short version: `go build ./...`, `go vet ./...`,
`go test ./...`, and a functional case for anything that touches
behaviour as PID 1.

## License

[Apache License 2.0](LICENSE)
