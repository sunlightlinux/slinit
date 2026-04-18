# slinit

A service manager and init system written in Go. The core is a port of
[dinit](https://github.com/davmac314/dinit), with features layered in
from [runit](http://smarden.org/runit/),
[s6-linux-init](https://skarnet.org/software/s6-linux-init/), and
[OpenRC](https://github.com/OpenRC/openrc).

slinit can run as PID 1 (init system) or as a user-level service
manager. It uses a dinit-compatible configuration format and manages
services with dependency tracking, automatic restart, and process
lifecycle management. Admins moving from any of the four upstreams
should keep their muscle memory:

- **dinit**: service-description format, dep types, state machine, and
  `slinitctl` verbs are 1:1 with dinit.
- **runit**: `finish-command`, `ready-check-command`, `pre-stop-hook`,
  `env-dir`, `control-command-<SIGNAL>`, `chroot`, `new-session`,
  `lock-file`, log rotation/filtering/processor, down-file marker,
  `once` command.
- **s6-linux-init**: catch-all logger, TAI64N/ISO timestamps,
  scheduled shutdown + cancel, wall messages, `/etc/shutdown.allow`
  access control, global boot-time rlimits, container mode with
  exit/halt codes + ready-fd, SysV compat symlinks, `slinit-init-maker`
  generator, `slinit-nuke` emergency, RT-signal container shutdown.
- **OpenRC**: `rc-service` / `rc-update` / `rc-status` CLI shims,
  `/etc/rc.conf` + `/etc/conf.d/<service>` sourcing for init.d scripts,
  named-runlevel dispatch (`init default|single|nonetwork|...`),
  init.d/LSB auto-detection.

Runlevels, where present, are pure UX aliases over the dependency
graph — slinit does not introduce a second state machine or config
format to accommodate them.

## Features

- **Service types**: process, scripted, bgprocess, internal, triggered
- **Dependency management**: 6 dependency types (regular, waits-for, milestone, soft, before, after)
- **Process lifecycle**: SIGTERM with configurable timeout, SIGKILL escalation
- **Auto-restart**: configurable restart policy with rate limiting and smooth recovery
- **Dinit-compatible config**: key=value service description files
- **Environment substitution**: `$VAR`, `${VAR}`, `${VAR:-default}`, `${VAR:+alt}`, `$$` escape in config files
- **Word-splitting expansion**: `$/VAR` splits variable value on whitespace into multiple command args
- **Service templates**: `name@argument` pattern with `$1` substitution in config
- **Config includes**: `@include` and `@include-opt` directives for modular config
- **Runit-inspired features**: finish-command, ready-check-command, pre-stop-hook, env-dir, control-command, chroot, new-session, lock-file, close-fds, log rotation/filtering/processor, down-file marker
- **Control socket**: binary protocol (v6) over Unix domain socket for runtime management
- **slinitctl CLI**: list, start, stop, wake, release, restart, status, is-started, is-failed, trigger, untrigger, signal, pause, continue, once, reload, unload, unpin, catlog, attach, setenv, unsetenv, getallenv, setenv-global, unsetenv-global, getallenv-global, add-dep, rm-dep, enable, disable, shutdown (with scheduled/cancel/status), graph, dependents, query-name, service-dirs, load-mech, boot-time, analyze
- **slinit-check**: offline and online config linter (validates executables, paths, dependencies; `--online` queries running daemon)
- **slinit-monitor**: event watcher + command executor (`%n`/`%s`/`%v` substitution)
- **Service aliases**: `provides` for alternative name lookup
- **Consumer pipes**: `consumer-of` to pipe output from one service into another
- **Log output**: buffer (in-memory, catlog), file (logfile with permissions/ownership + rotation/filtering), pipe (consumer-of)
- **Log rotation**: size-based, time-based, max files, log processor script, include/exclude pattern filtering
- **Ready notification**: pipefd/pipevar readiness protocol for services, ready-check-command polling
- **Socket activation**: pre-opened listening sockets passed to child (LISTEN_FDS=N convention), supports Unix/TCP/UDP (`tcp:host:port`, `udp:host:port`), multiple sockets via `+=`, on-demand activation
- **Hot reload**: reload service configuration from disk without restart
- **Service unload**: remove stopped services from memory
- **PID 1 init**: console setup, Ctrl+Alt+Del handling, child subreaper, orphan reaping
- **Process attributes**: nice, oom-score-adj, rlimits, ioprio, cgroup, cpu-affinity, no-new-privs, capabilities, securebits
- **Runtime environment**: setenv/unsetenv/getallenv via control socket, env-file loading (with `!clear`/`!unset`/`!import` meta-commands), env-dir (runit-style directory)
- **Process isolation**: chroot, new-session (setsid), lock-file (exclusive flock), close-stdin/stdout/stderr
- **Service lifecycle hooks**: finish-command (post-exit), pre-stop-hook (pre-SIGTERM), control-command (custom signal handlers)
- **Pause/continue**: SIGSTOP/SIGCONT via `slinitctl pause`/`continue` with control-command override
- **Down file**: `down` marker file prevents auto-start (cleared by explicit `slinitctl start`)
- **Once mode**: `slinitctl once` starts a service without auto-restart
- **Runtime dependencies**: add-dep/rm-dep, enable/disable via control socket
- **Enable-via**: `@meta enable-via` directive for default enable/disable source service
- **Push notifications**: SERVICEEVENT/ENVEVENT for real-time state and environment tracking
- **SIGUSR1 socket reopen**: recover control socket when filesystem becomes writable
- **slinit-shutdown**: standalone shutdown utility (also invocable as slinit-reboot, slinit-halt, slinit-soft-reboot via symlinks)
- **Shutdown**: orderly service stop, shutdown hooks, process cleanup (SIGTERM/SIGKILL), filesystem sync, reboot/halt/poweroff/kexec/softreboot
- **Soft-reboot**: restart slinit without rebooting the kernel (with shutdown hooks)
- **Kexec reboot**: reboot via kexec (skip firmware reinit, requires pre-loaded kernel)
- **Container mode**: `-o`/`--container` for Docker/LXC/Podman (SIGINT/SIGTERM → graceful halt)
- **Boot failure recovery**: interactive prompt or auto-recovery (`-r`) when all services stop without shutdown
- **Multiple boot services**: `-t svc1 -t svc2` or positional args to start multiple services at boot
- **Pass control socket**: `pass-cs-fd` passes a control connection fd to child processes
- **Readiness signaling**: `starts-rwfs` / `starts-log` flags for filesystem and logging readiness
- **UTMPX support**: `inittab-id`/`inittab-line` for session tracking, boot logging
- **/etc/init.d auto-detect**: automatic detection of SysV init scripts with LSB header parsing, BSD rc.d support
- **Cron-like periodic tasks**: `cron-command` with configurable interval, delay, and on-error behavior
- **Shutdown info display**: periodic reporter of blocking services during shutdown, escalating force shutdown (2nd signal reduces timeout, 3rd sends SIGKILL)
- **Parallel start limit**: soft concurrency control for service startup (`--parallel-start-limit`), slow-threshold filtering
- **Multi-service shared logger**: SharedLogMux multiplexes N service outputs into a single logger stdin with `[service-name]` line prefixes
- **Virtual TTY**: screen-like attach/detach for services via PTY allocation, ring buffer scrollback, Unix socket client multiplexing (`slinitctl attach`)
- **Boot-time clock guard**: prevents clock regression on systems without RTC / dead CMOS battery (compile-time floor + persistent timestamp file, similar to systemd-timesyncd)
- **Dual mode**: system init (PID 1) or user-level service manager
- **Offline enable/disable**: `--offline` mode creates/removes waits-for.d symlinks without a running daemon
- **Dinit naming compat**: `rlimit-addrspace`, `run-in-cgroup`, `consumer-of =` all supported as aliases
- **s6-linux-init features**:
  - Catch-all logger capturing early-boot stdout/stderr (`--catch-all-log`, `-B` to disable)
  - TAI64N / ISO-8601 / wallclock / none log timestamps (`--timestamp-format`)
  - Scheduled shutdown (`shutdown +5`, `shutdown HH:MM`) with cancel (`shutdown -c`) and status
  - Wall broadcasts to logged-in users on shutdown (disable with `--no-wall`)
  - `/etc/slinit/shutdown.allow` / `/etc/shutdown.allow` access control for signal-driven shutdown
  - Configurable `SIGTERM→SIGKILL` grace period (`--shutdown-grace`)
  - Global rlimits at boot, inherited by all services (`--rlimits nofile=65536,core=0,...`)
  - RT-signal container shutdown (SIGRTMIN+3..+6 → halt/poweroff/reboot/kexec)
  - UTMPX logout records for every active session + RUN_LVL shutdown boundary in wtmp
  - Kernel cmdline snapshot to `/run/slinit/kcmdline` (`--kcmdline-dest`)
  - `/run` tmpfs staging modes (`--run-mode=mount|remount|keep`)
  - Configurable devtmpfs mount point (`--devtmpfs-path`, empty disables)
  - `/sbin/halt`, `/sbin/poweroff`, `/sbin/reboot` compat via argv[0] dispatch
- **OpenRC UX compat**:
  - `rc-service <svc> <action>` — translates to `slinitctl start|stop|restart|status|...`
  - `rc-update add|del <svc> [runlevel]` — models runlevels as `runlevel-<name>` services
  - `rc-status [runlevel]` — lists services grouped by runlevel dep graph
  - Init.d scripts source `/etc/rc.conf` + `/etc/conf.d/<name>` automatically via `sh -c` wrapper
  - Named runlevel dispatch: `init default|single|nonetwork|boot|sysinit` → start `runlevel-<name>`
- **SysV compat**: `init 0` → poweroff, `init 6` → reboot, `init N` (1..5) → start runlevel-N
- **Standalone binaries**: `slinit-init-maker` (bootable layout generator), `slinit-nuke`
  (emergency `kill -1`), `slinit-shutdown` (orderly shutdown shim, also invocable as
  `slinit-reboot`/`slinit-halt`/`slinit-soft-reboot` symlinks)

## Building

```bash
# Core daemon + control CLI
go build ./cmd/slinit
go build ./cmd/slinitctl

# Companion utilities
go build ./cmd/slinit-check       # offline/online config linter
go build ./cmd/slinit-monitor     # event watcher + command executor
go build ./cmd/slinit-shutdown    # standalone shutdown utility
go build ./cmd/slinit-init-maker  # bootable service-dir generator
go build ./cmd/slinit-nuke        # emergency kill-all
go build ./cmd/slinit-mount       # autofs lazy-mount helper
go build ./cmd/slinit-checkpath   # path-validation helper

# OpenRC compat shims
go build ./cmd/rc-service
go build ./cmd/rc-update
go build ./cmd/rc-status

# Or build everything at once
go build ./...

# Optional compat symlinks:
ln -s slinit-shutdown slinit-reboot
ln -s slinit-shutdown slinit-halt
ln -s slinit-shutdown slinit-soft-reboot

# SysV compat (slinit itself handles these via argv[0]):
ln -s slinit /sbin/halt
ln -s slinit /sbin/poweroff
ln -s slinit /sbin/reboot
```

## Running

```bash
# User mode (default)
./slinit --services-dir /path/to/services

# System mode
./slinit --system --services-dir /etc/slinit.d

# Multiple boot services
./slinit -t network -t web-server -t database

# Container mode
./slinit --container -t myapp
```

### Command-line options (slinit)

| Flag | Description | Default |
|------|-------------|---------|
| `--services-dir` | Service description directory (comma-separated) | `~/.config/slinit.d` (user) or multiple system dirs |
| `--socket-path` | Control socket path | `~/.slinitctl` or `/run/slinit.socket` |
| `--system` / `-m` / `--system-mgr` | Run as system service manager | `false` |
| `--user` | Run as user service manager | `true` |
| `-t` / `--service` | Service to start at boot (repeatable, or use positional args) | `boot` |
| `-o` / `--container` | Run in container mode (Docker/LXC/Podman) | `false` |
| `--log-level` | Log level (debug, info, notice, warn, error) | `info` |
| `--console-level` | Minimum level for console output | inherits `--log-level` |
| `-q` / `--quiet` | Suppress all but error output | `false` |
| `-r` / `--auto-recovery` | Auto-start `recovery` service on boot failure (PID 1) | `false` |
| `-e` / `--env-file` | Environment file to load at startup | |
| `-F` / `--ready-fd` | File descriptor to notify when boot service is ready | `-1` |
| `-l` / `--log-file` | Log to file instead of console | |
| `-b` / `--cgroup-path` | Default cgroup base path for services | |
| `--parallel-start-limit` | Max concurrent service starts (0 = unlimited) | `0` |
| `--parallel-start-slow-threshold` | Seconds before a starting service is considered "slow" | `10s` |
| `--shutdown-grace` | SIGTERM→SIGKILL grace period during shutdown | `3s` |
| `--no-wall` | Disable wall broadcasts at shutdown | `false` |
| `--banner` | Boot banner printed to console (empty disables) | `slinit booting...` |
| `--umask` | Initial umask (octal) | `0022` |
| `-1` / `--console-dup` | Duplicate log output to `/dev/console` even with `--log-file` | `false` |
| `--catch-all-log` | Path for the early-boot catch-all log | `/run/slinit/catch-all.log` |
| `-B` / `--no-catch-all` | Disable catch-all logger | `false` |
| `--timestamp-format` | Log timestamp format (`wallclock`\|`iso`\|`tai64n`\|`none`) | `wallclock` |
| `--rlimits` | Global rlimits applied to slinit and inherited by services (`name=soft[:hard]` comma-separated) | |
| `--run-mode` | Stage `/run` at boot: `mount` (fresh tmpfs), `remount` (unmount+mount), `keep` (untouched) | `mount` |
| `--devtmpfs-path` | Mount devtmpfs at this path (empty disables) | `/dev` |
| `--kcmdline-dest` | Snapshot `/proc/cmdline` to this path (empty disables) | `/run/slinit/kcmdline` |
| `-S` / `--sys` | Override platform detection (`docker`, `lxc`, `podman`, `wsl`, `xen0`, `xenu`, `none`) | auto |
| `--conf-dir` | Override `conf.d` overlay directories (comma-separated; `none` disables overlays) | |
| `--version` | Show version and exit | |

Default service directories (when `--services-dir` is not set):
- **System mode**: `/etc/slinit.d`, `/run/slinit.d`, `/usr/local/lib/slinit.d`, `/lib/slinit.d`
- **User mode**: `$XDG_CONFIG_HOME/slinit.d` (or `~/.config/slinit.d`), `/etc/slinit.d/user`, `/usr/lib/slinit.d/user`, `/usr/local/lib/slinit.d/user`

## Service configuration

Service files use a dinit-compatible format:

```ini
# /etc/slinit.d/myservice
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

Example bgprocess service:

```ini
# /etc/slinit.d/mydaemon
type = bgprocess
command = /usr/sbin/mydaemon
pid-file = /run/mydaemon.pid
stop-timeout = 15
depends-on: network
```

Example service with logfile output:

```ini
# /etc/slinit.d/myapp
type = process
command = /usr/bin/myapp
log-type = file
logfile = /var/log/myapp.log
logfile-permissions = 0640
logfile-uid = 1000
logfile-gid = 1000
```

Example service with process attributes and capabilities:

```ini
# /etc/slinit.d/worker
type = process
command = /usr/bin/worker
nice = 10
oom-score-adj = 500
ioprio = be:4
cpu-affinity = 0-3
rlimit-nofile = 1024:4096
rlimit-core = unlimited
cgroup = /sys/fs/cgroup/workers
capabilities = cap_net_bind_service,cap_sys_nice
securebits = noroot keep-caps
options = no-new-privs
env-file = /etc/worker.env
run-as = worker:worker
```

Example service with runit-inspired features:

```ini
# /etc/slinit.d/webapp
type = process
command = /usr/bin/webapp
finish-command = /usr/local/bin/cleanup.sh
ready-check-command = /usr/bin/curl -sf http://localhost:8080/health
ready-check-interval = 0.5
pre-stop-hook = /usr/local/bin/drain-connections.sh
control-command-HUP = /usr/local/bin/graceful-reload.sh
env-dir = /etc/webapp/env.d
chroot = /srv/webapp
new-session = true
lock-file = /run/webapp.lock
log-type = file
logfile = /var/log/webapp.log
logfile-max-size = 10000000
logfile-max-files = 5
logfile-rotate-time = 86400
log-processor = /usr/bin/gzip
log-exclude = DEBUG
restart = on-failure
depends-on: network
```

Example consumer pipe (service B reads service A stdout):

```ini
# /etc/slinit.d/producer
type = process
command = /usr/bin/generate-data
log-type = pipe

# /etc/slinit.d/consumer
type = process
command = /usr/bin/process-data
consumer-of: producer
```

Example service template (`myservice@` base file):

```ini
# /etc/slinit.d/myservice@
type = process
command = /usr/bin/myservice --instance $1
working-dir = /var/lib/myservice/${1}
depends-on: network
```

Start with `slinitctl start myservice@web` — `$1` is replaced with `web`.

Example multi-service shared logger:

```ini
# /etc/slinit.d/central-logger
type = process
command = /usr/bin/multilog t ./log

# /etc/slinit.d/app-one
type = process
command = /usr/bin/app-one
shared-logger = central-logger

# /etc/slinit.d/app-two
type = process
command = /usr/bin/app-two
shared-logger = central-logger
```

Logger receives lines prefixed: `[app-one] ...`, `[app-two] ...`.

Example service with virtual TTY:

```ini
# /etc/slinit.d/interactive-svc
type = process
command = /usr/bin/myapp
vtty = true
vtty-scrollback = 131072
```

Attach with `slinitctl attach interactive-svc` (Ctrl+] to detach).

Example service with cron task:

```ini
# /etc/slinit.d/worker
type = process
command = /usr/bin/worker
cron-command = /usr/bin/cleanup-temp
cron-interval = 3600
cron-delay = 60
cron-on-error = continue
```

Example with `@meta enable-via`:

```ini
# /etc/slinit.d/optional-svc
type = process
command = /usr/bin/optional
@meta enable-via mygroup
```

`slinitctl enable optional-svc` will add a waits-for dep from `mygroup` instead of `boot`.

### Configuration reference

| Option                    | Description                                      |
|---------------------------|--------------------------------------------------|
| `type`                    | Service type (process, bgprocess, scripted, internal, triggered) |
| `command`                 | Command to run (supports `+=` to append)         |
| `stop-command`            | Command to run on stop (scripted, supports `+=`)  |
| `depends-on:`             | Hard dependency                                  |
| `depends-ms:`             | Milestone dependency (must start, then becomes soft) |
| `waits-for:`              | Soft dependency (wait for start/fail)            |
| `before:`                 | Ordering: start before target                    |
| `after:`                  | Ordering: start after target                     |
| `provides`                | Alias name for service lookup                    |
| `consumer-of` / `consumer-of:` | Pipe output from named service into this one (= or :) |
| `restart`                 | Auto-restart mode (yes, on-failure, no)          |
| `restart-delay`           | Seconds to wait before restarting                |
| `restart-limit-count`     | Max restarts within interval                     |
| `restart-limit-interval`  | Interval (seconds) for restart limit             |
| `log-type`                | Output logging (buffer, file, pipe, none)        |
| `logfile`                 | Log file path (when log-type = file)             |
| `log-buffer-size`         | Log buffer size in bytes (when log-type = buffer)|
| `logfile-permissions`     | Log file permissions, octal (default 0600)       |
| `logfile-uid`             | Log file owner UID                               |
| `logfile-gid`             | Log file owner GID                               |
| `ready-notification`      | Readiness protocol (pipefd:N, pipevar:VARNAME)   |
| `socket-listen`           | Pre-opened listening socket(s) passed to child (LISTEN_FDS), supports `+=` for multiple, `tcp:`/`udp:` prefix |
| `socket-activation`       | Activation mode: `immediate` (default) or `on-demand` |
| `socket-permissions`      | Socket file permissions                          |
| `socket-uid/gid`          | Socket file ownership                            |
| `pid-file`                | PID file path (bgprocess type)                   |
| `start-timeout`           | Timeout for service start (seconds)              |
| `stop-timeout`            | Timeout for service stop (seconds)               |
| `options`                 | Service flags (runs-on-console, unmask-intr, no-new-privs, etc.) |
| `term-signal`             | Signal for graceful stop                         |
| `working-dir`             | Working directory for the process                |
| `run-as`                  | Run command as user:group                        |
| `env-file`                | Environment variables file (KEY=VALUE, `!clear`, `!unset`, `!import`) |
| `env-dir`                 | Runit-style env directory (one file per var)      |
| `finish-command`          | Command run after process exit (before restart)   |
| `ready-check-command`     | Polling readiness check (alternative to pipefd)   |
| `ready-check-interval`    | Polling interval for ready-check (default 1s)     |
| `pre-stop-hook`           | Command run before SIGTERM (receives PID as arg)  |
| `control-command-SIGNAL`  | Custom signal handler (e.g., control-command-HUP) |
| `chroot`                  | Chroot directory before exec                      |
| `new-session`             | Create new session (setsid) for the process       |
| `lock-file`               | Exclusive flock file (prevents duplicate instances)|
| `close-stdin`             | Close stdin (redirect to /dev/null)               |
| `close-stdout`            | Close stdout (redirect to /dev/null)              |
| `close-stderr`            | Close stderr (redirect to /dev/null)              |
| `logfile-max-size`        | Rotate logfile at this size (bytes)               |
| `logfile-max-files`       | Max rotated log files to keep                     |
| `logfile-rotate-time`     | Rotate logfile at time interval (seconds)         |
| `log-processor`           | Command run on each rotated logfile               |
| `log-include`             | Regex: only write matching lines to log           |
| `log-exclude`             | Regex: drop matching lines from log               |
| `chain-to`                | Service to start after this one stops            |
| `nice`                    | Process scheduling priority (-20..19)            |
| `oom-score-adj`           | OOM killer score adjustment (-1000..1000)        |
| `ioprio`                  | I/O priority class:level (be:4, rt:0, idle)      |
| `cpu-affinity`            | CPU affinity mask (0-3, 0 1 2, 0,2,4)            |
| `cgroup`                  | Cgroup path for the child process                |
| `rlimit-nofile`           | File descriptor limit (soft:hard or unlimited)   |
| `rlimit-core`             | Core dump size limit (soft:hard or unlimited)    |
| `rlimit-data`             | Data segment size limit (soft:hard or unlimited) |
| `rlimit-as`               | Address space limit (soft:hard or unlimited)     |
| `rlimit-addrspace`        | Alias for `rlimit-as` (dinit compat)             |
| `run-in-cgroup`           | Alias for `cgroup` (dinit compat)                |
| `capabilities`            | Ambient capabilities (cap_net_bind_service, etc.)|
| `securebits`              | Securebits flags (noroot, keep-caps, etc.)       |
| `inittab-id`              | UTMPX inittab ID for session tracking            |
| `inittab-line`            | UTMPX inittab line for session tracking          |
| `load-options`            | Loader flags (export-passwd-vars, export-service-name) |
| `@meta enable-via`        | Default "from" service for enable/disable        |
| `shared-logger`           | Name of shared logger service (multi-service → single logger) |
| `vtty`                    | Enable virtual TTY for screen-like attach/detach  |
| `vtty-scrollback`         | VirtualTTY scrollback buffer size in bytes (default 64KB) |
| `cron-command`            | Periodic command to execute while service is running |
| `cron-interval`           | Interval between cron executions (seconds)        |
| `cron-delay`              | Initial delay before first cron execution (seconds) |
| `cron-on-error`           | Behavior on cron command failure: `continue` (default) or `stop` |
| `@include`                | Include another config file (error if not found) |
| `@include-opt`            | Include another config file (ignore if not found)|

### Service types

| Type | Description |
|------|-------------|
| `process` | Long-running daemon managed by slinit |
| `scripted` | Service controlled by start/stop commands |
| `internal` | Milestone service with no associated process |
| `bgprocess` | Self-backgrounding daemon (forks, writes PID file, monitored via polling) |
| `triggered` | Service that waits for an external trigger before completing startup |

### Dependency types

| Directive | Description |
|-----------|-------------|
| `depends-on` | Hard dependency -- start required, stop propagates |
| `depends-ms` | Milestone dependency -- must start, then becomes soft |
| `waits-for` | Soft dependency -- waits for start, but failure doesn't propagate |
| `before` | Ordering -- this service starts before the named service |
| `after` | Ordering -- this service starts after the named service |

### Environment variable substitution

Config values support environment variable expansion:

| Syntax | Description |
|--------|-------------|
| `$VAR` | Expand variable |
| `${VAR}` | Expand variable (explicit braces) |
| `${VAR:-default}` | Use default if VAR is empty/unset |
| `${VAR:+alt}` | Use alt if VAR is set and non-empty |
| `$$` | Literal `$` |
| `$/VAR` | Word-split: expand and split on whitespace into multiple args |
| `$1` / `${1}` | Service template argument (for `name@arg` services) |

## Control CLI (slinitctl)

`slinitctl` communicates with a running slinit instance via the control socket.

### Global options

| Flag | Description |
|------|-------------|
| `--socket-path`, `-p` | Control socket path |
| `--system`, `-s` | Connect to system service manager |
| `--user`, `-u` | Connect to user service manager |
| `--no-wait` | Suppress output (fire-and-forget) |
| `--pin` | Pin service in started/stopped state (start/stop) |
| `--force`, `-f` | Force stop even with dependents (stop/restart) |
| `--ignore-unstarted` | Exit 0 if service already stopped (stop/restart) |
| `--offline`, `-o` | Offline mode for enable/disable without daemon |
| `--services-dir`, `-d` | Service directory for offline mode |
| `--from <service>` | Source service for enable/disable (default: enable-via or boot) |
| `--use-passed-cfd` | Use fd from `SLINIT_CS_FD` env var |

### Commands

```bash
# List all loaded services
slinitctl list

# Start/stop/restart
slinitctl start myservice           # start and mark active
slinitctl start --pin myservice     # start and pin in started state
slinitctl wake myservice            # start without marking active
slinitctl stop myservice            # stop
slinitctl stop --force myservice    # force stop (even with dependents)
slinitctl stop --pin myservice      # stop and pin in stopped state
slinitctl release myservice         # unmark active (stop if unrequired)
slinitctl restart myservice
slinitctl restart --force myservice # force restart
slinitctl unpin myservice           # remove start/stop pins

# Service status
slinitctl status myservice
slinitctl is-started myservice      # exit 0 if started, 1 otherwise
slinitctl is-failed myservice       # exit 0 if failed, 1 otherwise

# Trigger / untrigger
slinitctl trigger mytrigger
slinitctl untrigger mytrigger       # reset trigger state

# Send signal to a service process
slinitctl signal HUP myservice

# Pause/continue (SIGSTOP/SIGCONT)
slinitctl pause myservice
slinitctl continue myservice

# Start once (no auto-restart)
slinitctl once myservice

# View buffered service output
slinitctl catlog myservice
slinitctl catlog --clear myservice

# Attach to service virtual TTY (screen-like, Ctrl+] to detach)
slinitctl attach myservice

# Reload config from disk (without restart)
slinitctl reload myservice

# Unload a stopped service from memory
slinitctl unload myservice

# Runtime environment management
slinitctl setenv myservice KEY=VALUE
slinitctl unsetenv myservice KEY
slinitctl getallenv myservice

# Runtime dependency management
slinitctl add-dep myservice depends-on otherservice
slinitctl rm-dep myservice waits-for otherservice

# Enable/disable (add/remove waits-for dep on boot or enable-via service)
slinitctl enable myservice
slinitctl enable --from mygroup myservice   # enable from a specific service
slinitctl disable myservice

# Offline enable/disable (without daemon, operates on waits-for.d symlinks)
slinitctl --offline enable myservice
slinitctl --offline --services-dir /etc/slinit.d disable myservice
slinitctl --offline --from mygroup enable myservice

# Query service dependents
slinitctl dependents myservice

# Query loader mechanism
slinitctl query-load-mech

# Boot timing analysis
slinitctl boot-time

# Initiate system shutdown
slinitctl shutdown poweroff
slinitctl shutdown reboot
slinitctl shutdown halt
slinitctl shutdown softreboot       # restart slinit without kernel reboot

# Scheduled shutdown + cancel
slinitctl shutdown reboot +5        # in 5 minutes
slinitctl shutdown poweroff 18:30   # at 18:30 today/tomorrow
slinitctl shutdown -c               # cancel a pending shutdown
slinitctl shutdown --status         # report pending shutdown, if any

# Dependency inspection
slinitctl graph myservice           # dep graph rooted at myservice
slinitctl analyze                   # global dep-graph overview

# Connect to system/user instance explicitly
slinitctl --system list
slinitctl --user list
slinitctl -p /tmp/test.socket list  # custom socket path
```

## Companion Tools

### slinit-check

Configuration linter. Validates service files offline or using a running daemon's context:

```bash
# Offline mode (default)
slinit-check -d /etc/slinit.d myservice

# Online mode (queries running daemon for service dirs and env)
slinit-check --online myservice
slinit-check --online -p /run/slinit.ctl myservice
```

Checks: file existence, type validity, command executability, dependency references, circular dependencies, depth limits.

### slinit-monitor

Event watcher that subscribes to service state changes and optionally executes commands:

```bash
# Watch all events
slinit-monitor

# Execute command on state change (%n=name, %s=state, %v=event)
slinit-monitor -c 'echo "Service %n changed to %s (event: %v)"'
```

### slinit-init-maker

Generates a bootable service-description directory skeleton — top-level
`boot` target, optional `system-mounts` + `network` stubs, N agetty
services (with correct inittab-id), env-file with `HOSTNAME`/`TZ`/`PATH`,
optional shutdown-hook sample, README. Inspired by
[s6-linux-init-maker](https://skarnet.org/software/s6-linux-init/s6-linux-init-maker.html).

```bash
# Default layout to /etc/slinit/boot.d
slinit-init-maker

# Custom layout: 4 ttys, specific hostname/tz, no network stub
slinit-init-maker --ttys 4 --hostname myhost --tz Europe/Bucharest \
    --output /tmp/boot.d --with-shutdown-hook

# Preview without touching the disk
slinit-init-maker --dry-run
```

### slinit-nuke

Emergency userspace cleanup: `kill(-1, SIGTERM)` → grace period →
`kill(-1, SIGKILL)`. Intended for recovery scenarios where the orderly
shutdown path is unavailable — not a replacement for
`slinitctl shutdown`.

```bash
slinit-nuke                    # TERM, wait 2s, KILL
slinit-nuke --grace 500ms
slinit-nuke -9                 # skip TERM, SIGKILL immediately
```

### slinit-shutdown

Standalone shutdown utility. Can talk to a running slinit or — with
`--system` — perform the shutdown sequence directly. Invocable as
`slinit-reboot`, `slinit-halt`, or `slinit-soft-reboot` via symlinks.

```bash
slinit-shutdown -r            # reboot
slinit-shutdown -p            # poweroff
slinit-shutdown -h            # halt
slinit-shutdown -s            # soft reboot
slinit-shutdown -k            # kexec
```

### OpenRC compat: rc-service / rc-update / rc-status

Thin argv-translating shims over `slinitctl` for admins used to
OpenRC. They exec `slinitctl` (resolved via `$PATH` or the `SLINITCTL`
env var), so output, exit codes and flag precedence mirror
`slinitctl`'s own.

```bash
# rc-service — service control
rc-service nginx start
rc-service nginx stop
rc-service nginx status
rc-service --exists nginx     # → slinitctl is-started nginx
rc-service --list             # → slinitctl list

# rc-update — runlevel membership (modelled as runlevel-<name> services)
rc-update add  nginx default  # → slinitctl --from runlevel-default enable nginx
rc-update del  nginx boot     # → slinitctl --from runlevel-boot disable nginx
rc-update show                # → slinitctl graph runlevel-default
rc-update update              # no-op (slinit has no dep cache)

# rc-status — status listing
rc-status                     # → slinitctl list
rc-status default             # → slinitctl graph runlevel-default
rc-status --list              # list known OpenRC runlevel names
rc-status --runlevel          # print "default" (slinit has no current runlevel)
```

`/etc/rc.conf` and `/etc/conf.d/<name>` are sourced automatically
before every init.d script action (see Project structure notes),
so OpenRC per-service config files like `/etc/conf.d/nginx` keep
working unchanged.

## Architecture

slinit follows Go-idiomatic patterns while preserving dinit's proven service management design:

- **Goroutines + channels** replace dinit's dasynq event loop
- **Interface + struct embedding** replaces C++ virtual method dispatch
- **Two-phase state transitions** (propagation + execution) preserve correctness from dinit
- **One goroutine per child process** for monitoring, with channel-based notification
- **Binary control protocol** (v6) over Unix domain sockets, goroutine-per-connection
- **Push notifications**: SERVICEEVENT5/ENVEVENT for real-time tracking
- **PID 1 shutdown sequence**: shutdown hooks, process cleanup, filesystem sync, reboot syscalls

### PID 1 Signal Handling

| Signal        | Action                | Source                        |
|---------------|-----------------------|-------------------------------|
| `SIGTERM`     | reboot                | busybox `reboot`              |
| `SIGINT`      | reboot                | Ctrl-Alt-Del (via CAD)        |
| `SIGQUIT`     | poweroff              | --                            |
| `SIGUSR1`     | reopen control socket | recovery when fs writable     |
| `SIGUSR2`     | poweroff              | busybox `poweroff`            |
| `SIGHUP`      | ignored               | --                            |
| `SIGCHLD`     | reap orphans          | child process exit            |
| `SIGRTMIN+3`  | halt                  | systemd-compat container      |
| `SIGRTMIN+4`  | poweroff              | systemd-compat container      |
| `SIGRTMIN+5`  | reboot                | systemd-compat container      |
| `SIGRTMIN+6`  | kexec                 | systemd-compat container      |

RT signals let `kill -s RTMIN+4 1` from inside a container trigger a
clean shutdown without needing slinitctl present in the image.

Signal-driven shutdown (SIGTERM/SIGINT/SIGQUIT/SIGUSR2/SIGRTMIN+3..+6)
can be gated by `/etc/slinit/shutdown.allow` — see [Features](#features)
above. The gate applies only to the initial trigger; a second press
of Ctrl+Alt+Del or a repeated RT signal always escalates.

## Project structure

```
slinit/
├── cmd/
│   ├── slinit/            # Daemon entry point (incl. SysV argv[0] dispatch)
│   ├── slinitctl/         # Control CLI (36 commands, incl. attach)
│   ├── slinit-check/      # Config linter (offline + online)
│   ├── slinit-monitor/    # Event watcher + command executor
│   ├── slinit-shutdown/   # Standalone shutdown utility (+ reboot/halt/soft symlinks)
│   ├── slinit-init-maker/ # Bootable service-dir generator (s6-linux-init-maker inspired)
│   ├── slinit-nuke/       # Emergency kill-all (TERM → grace → KILL)
│   ├── slinit-mount/      # Autofs lazy-mount helper
│   ├── slinit-checkpath/  # Path-validation helper
│   ├── rc-service/        # OpenRC compat: thin shim over slinitctl
│   ├── rc-update/         # OpenRC compat: runlevel membership via runlevel-<name> services
│   └── rc-status/         # OpenRC compat: status listing
├── pkg/
│   ├── service/           # Service types, state machine, dependency graph
│   ├── config/            # Dinit-compatible config parser + loader, init.d/LSB, OpenRC conf.d wrapper
│   ├── control/           # Control socket protocol (v6) and server
│   ├── shutdown/          # PID 1 init, shutdown executor, soft-reboot, clock guard, run-mode
│   ├── process/           # Process execution, monitoring, attrs, caps
│   ├── eventloop/         # Event loop, signals, timers
│   ├── logging/           # Console logger (wallclock / ISO / TAI64N / none)
│   ├── utmp/              # UTMPX cgo wrapper (boot + logout + shutdown records)
│   ├── autofs/            # Autofs direct-mount helper
│   ├── checkpath/         # Path permission / ownership verifier
│   └── platform/          # Container/platform auto-detect (docker/lxc/podman/wsl/xen)
├── internal/util/         # Path and parsing utilities
├── completions/           # Shell completions (bash, zsh, fish)
├── demo/                  # QEMU demo environment
├── tests/functional/      # 52 QEMU-based integration tests
├── tests/fuzz/            # 21 fuzz targets (config, protocol, autofs, process parsers)
└── tests/performance/     # Performance and stress harness
```

## Testing

```bash
# Unit tests (~751 tests + benchmarks across 21 packages)
go test ./...

# Functional tests (52 QEMU-based integration tests)
./tests/functional/run-tests.sh

# Fuzz targets (18 targets across 4 files)
go test -fuzz=FuzzParseConfig ./tests/fuzz
```

## Roadmap

- [x] **Phase 1**: Foundation -- types, state machine, config parser, event loop
- [x] **Phase 2**: Process services -- fork/exec, child monitoring, restart logic
- [x] **Phase 3**: Full dependency graph -- all 6 dep types, TriggeredService, BGProcessService
- [x] **Phase 4**: Control protocol + `slinitctl` CLI
- [x] **Phase 5**: PID 1 mode + shutdown sequence
- [x] **Phase 6**: Advanced features -- catlog, reload, ready-notification, socket activation, provides, unload, consumer-of, logfile, wake/release, is-started/is-failed, setenv/getallenv, add-dep/rm-dep, enable/disable, nice/oom/ioprio/cgroup/rlimits, capabilities/securebits, unmask-intr, auto-recovery, starts-rwfs/starts-log, pass-cs-fd, kexec
- [x] **Phase 7**: Container mode, env substitution, shutdown hooks, UTMPX
- [x] **Phase 8**: CLI flags batch -- unpin, softreboot, --system/-s/--user/-u, --no-wait/--pin/--force, --ignore-unstarted, --offline/-o/--services-dir/-d, --use-passed-cfd/--from, multiple default service dirs
- [x] **Phase 9**: Daemon flags -- --system-mgr, --env-file, --ready-fd, --log-file, --cgroup-path, SLINIT_SERVICENAME/SLINIT_SERVICEDSCDIR, advanced env substitution, command +=, load-options, kernel cmdline filtering
- [x] **Phase 10**: Push notifications -- SERVICEEVENT, LISTENENV/ENVEVENT, mutex-serialized writes
- [x] **Phase 11**: Protocol v5 -- LISTSERVICES5/SERVICESTATUS5/SERVICEEVENT5, slinit-check, slinit-monitor, @include/@include-opt
- [x] **Phase 12**: Complete dinit parity -- @meta, env-file meta-commands, PINNEDSTOPPED/PINNEDSTARTED, SERVICE_DESC_ERR/SERVICE_LOAD_ERR, PREACK, QUERY_LOAD_MECH, DEPENDENTS, $/NAME word-splitting, service templates (name@arg), @meta enable-via, SIGUSR1 socket reopen, soft-reboot shutdown hooks
- [x] **Phase 13**: Runit-inspired features -- finish-command, ready-check-command, pre-stop-hook, env-dir, control-command (custom signal handlers), chroot, new-session, lock-file, close-fds, pause/continue, log rotation (size/time/max-files), log filtering (include/exclude regex), log processor, down-file marker, once command
- [x] **Phase 14**: /etc/init.d auto-detect with LSB header parsing, BSD rc.d support
- [x] **Phase 15**: Shutdown info display, escalating force shutdown, cron-like periodic tasks, soft parallel start limit, proper socket activation (multiple sockets, TCP/UDP, on-demand)
- [x] **Phase 16**: Multi-service shared logger (SharedLogMux -- N producers → single logger, line-prefixed)
- [x] **Phase 17**: Virtual TTY -- screen-like attach/detach via PTY allocation, ring buffer scrollback, `slinitctl attach`
- [x] **Phase 18**: s6-linux-init parity -- catch-all logger, TAI64N/ISO/none timestamps, scheduled shutdown + cancel + status, wall broadcasts, `/etc/shutdown.allow` access control, configurable grace, global rlimits, RT-signal container shutdown (SIGRTMIN+3..+6), UTMPX logout + wtmp RUN_LVL shutdown, kernel cmdline snapshot, `/run` tmpfs run-mode, configurable devtmpfs, SysV argv[0] compat (`halt`/`poweroff`/`reboot`), `slinit-init-maker`, `slinit-nuke`
- [x] **Phase 19**: OpenRC UX compat -- `rc-service`/`rc-update`/`rc-status` argv shims, `/etc/rc.conf` + `/etc/conf.d/<name>` sourcing via `sh -c` wrapper, runlevels modelled as `runlevel-<name>` services, named-runlevel dispatch (`init default|single|nonetwork|boot|sysinit`)

## License

[Apache License 2.0](LICENSE)
