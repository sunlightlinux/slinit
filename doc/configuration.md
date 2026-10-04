# slinit configuration reference

Service description files and the daemon's command-line surface.

The authoritative references are the man pages:
[slinit-service(5)](man/slinit-service.5.md) for every directive and
[slinit(8)](man/slinit.8.md) for every flag. This file is the worked
version — examples by service shape, then the tables.
[doc/features.md](features.md) is the generated list of everything the
parser and control protocol accept.

The README keeps a short version of the examples below; this is the long
one, split out when the README was trimmed for the 3.0 line.

## Examples by service shape

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

## Configuration reference

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
| `socket-reuseport`        | `SO_REUSEPORT` on `tcp:`/`udp:` listeners, so N template instances can share one hot port ([guide](doc/operators-guide.md#scaling-a-hot-port-across-workers)) |
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
| `slice`                   | Hierarchical cgroup parent, systemd-style         |
| `delegate`                | Hand the cgroup subtree to the service itself     |
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
| `cron-calendar`           | systemd-style `OnCalendar=` expression (`daily`, `Mon..Fri 09:00`, `*:0/15`) |
| `cron-randomized-delay`   | Upper bound on jitter added to each fire, drawn from `[0,d)`; applies to interval and calendar modes |
| `cron-fixed-random-delay` | Draw that offset once from the machine-id instead of per fire (systemd `FixedRandomDelay=`) |
| `cron-persistent`         | Catch-up run when a fire was missed; last-run instant kept on disk, so it survives a reboot |
| `prepared-by:`            | Hard dependency that also restarts when the dependent restarts |
| `condition-*` / `assert-*` | Systemd-style start predicates (13 kinds, `!` negation) -- skip silently / fail start |
| `runtime-directory`       | systemd-style auto-managed `/run/<svc>` (chowned to run-as) |
| `state-directory`         | Persistent `/var/lib/<svc>` (also `cache-`/`logs-`/`configuration-directory`) |
| `private-tmp`             | Per-service `/tmp` and `/var/tmp` tmpfs           |
| `protect-system`          | RO-remount `/usr`/`/boot`/`/efi` (yes/full/strict) |
| `read-only-paths`         | Bind ro at given paths                            |
| `read-write-paths`        | Bind rw at given paths (punches holes through protect-system) |
| `inaccessible-paths`      | Hide paths via empty tmpfs mount                  |
| `bind-paths`              | Bind host paths into the sandbox (rw)             |
| `bind-read-only-paths`    | Bind host paths into the sandbox (ro)             |
| `temporary-filesystem`    | Mount fresh tmpfs at given path                   |
| `protect-home`            | yes/tmpfs/read-only on `/home`,`/root`,`/run/user`|
| `protect-proc`            | proc-hidepid mode (`default`/`invisible`/`ptraceable`) |
| `proc-subset`             | `/proc` view subset (`all`/`pid`)                 |
| `system-call-filter`      | seccomp allow/deny list; supports `@group` and `~deny-first` |
| `system-call-architectures` | Allowed architectures for seccomp                |
| `system-call-error-number` | Errno returned for filtered syscalls (default EPERM) |
| `protect-kernel-tunables` | Block writes to `/proc/sys`, `/sys`               |
| `protect-kernel-modules`  | Block `init_module`/`finit_module`/`delete_module` |
| `protect-kernel-logs`     | Block `syslog`                                    |
| `protect-clock`           | Block `clock_settime`/`settimeofday`/`adjtimex`   |
| `protect-control-groups`  | RO `/sys/fs/cgroup`                               |
| `protect-hostname`        | Block `sethostname`/`setdomainname`               |
| `lock-personality`        | Block `personality` syscall                       |
| `failure-action`          | System action on permanent failure: none/reboot/poweroff/halt/exit |
| `success-action`          | System action on clean finish: none/reboot/poweroff/halt/exit |
| `reboot-argument`         | Argument for reboot syscall (kexec-style)        |
| `runtime-max-sec`         | Hard cap on STARTED time; stop when exceeded     |
| `oom-policy`              | Reaction to cgroup-v2 OOM kill: continue/stop/kill |
| `pre-start-command`       | Hook before `command` (sync, non-zero exit fails start) |
| `post-start-command`      | Hook after Started (async, log-only)             |
| `log-rate-limit-interval` / `-burst` | Token-bucket limiter (drop excess lines) |
| `log-level-max`           | Drop lines above syslog severity (emerg..debug)  |
| `load-credential`         | `NAME:PATH` copy a file into `/run/credentials/<svc>/` |
| `set-credential`          | `NAME:VALUE` write inline literal as a credential |
| `dynamic-user`            | Allocate a transient UID/GID per BringUp, release on Stopped |
| `file-descriptor-store-max` | Enable sd_notify FDSTORE=1 fd handover across restarts |
| `bundle-of`               | s6-rc-style grouping: names a set of services this internal svc pulls up as a unit; accepts comma-/space-separated list or repeated directive |
| `log-select`              | s6-log-style regex chain (`-* +alert +warn`); last-matched verdict wins per line; mutually exclusive with log-include / log-exclude |
| `@include`                | Include another config file (error if not found) |
| `@include-opt`            | Include another config file (ignore if not found)|

## Service types

| Type | Description |
|------|-------------|
| `process` | Long-running daemon managed by slinit |
| `scripted` | Service controlled by start/stop commands |
| `internal` | Milestone service with no associated process |
| `bgprocess` | Self-backgrounding daemon (forks, writes PID file, monitored via polling) |
| `triggered` | Service that waits for an external trigger before completing startup |

## Dependency types

| Directive | Description |
|-----------|-------------|
| `depends-on` | Hard dependency -- start required, stop propagates |
| `depends-ms` | Milestone dependency -- must start, then becomes soft |
| `waits-for` | Soft dependency -- waits for start, but failure doesn't propagate |
| `prepared-by` | Hard dependency like `depends-on`, but each restart of the dependent also restarts the dependency (for prepare/cleanup per execution) |
| `before` | Ordering -- this service starts before the named service |
| `after` | Ordering -- this service starts after the named service |

## Environment variable substitution

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


## Appendix: daemon command-line options

Kept here for convenience. [slinit(8)](man/slinit.8.md) is authoritative
and documents all 46 flags.

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
| `--emergency-timeout` | Max time slinit waits for services to drain during shutdown before the force-exit path (SIGKILL any straggler, log names of blocking services in the same error line, then reboot syscall). Tune up for heavy stop cascades (docker + full systemd-style graph) | `90s` |
| `--persist-intent` | Directory where pin transitions are persisted; `stop --pin X` writes `<dir>/X` with `pinned-stopped` so the pin survives a reboot. Empty disables (opt-in). Recommended: `/var/lib/slinit/intent` | (empty) |
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
| `-S` / `--sys` | Override platform detection (`docker`, `lxc`, `podman`, `systemd-nspawn`, `openvz`, `vserver`, `rkt`, `uml`, `wsl`, `xen0`, `xenu`, `kvm`, `qemu`, `vmware`, `microsoft` (Hyper-V), `oracle` (VirtualBox), `bochs`, `none`) | auto |
| `--conf-dir` | Override `conf.d` overlay directories (comma-separated; `none` disables overlays) | |
| `-a` / `--cpu-affinity` | Default CPU affinity for daemon and services (e.g. `0-3`, `0,2,4`) | |
| `--restore-from-snapshot` | Replay operator-intent snapshot after soft-reboot (path to snapshot file) | |
| `--watchdog-device` | Hardware watchdog character device to feed (PID 1 / container mode) | auto (`/dev/watchdog0` → `/dev/watchdog`) |
| `--watchdog-timeout` | Kernel-side watchdog timeout (`WDIOC_SETTIMEOUT`) | `60s` |
| `--watchdog-interval` | How often the feeder pings the device | `timeout / 3` |
| `--no-watchdog` | Disable hardware-watchdog feeder even when PID 1 | `false` |
| `--version` | Show version and exit | |

Default service directories (when `--services-dir` is not set):
- **System mode**: `/etc/slinit.d`, `/run/slinit.d`, `/usr/local/lib/slinit.d`, `/lib/slinit.d`
- **User mode**: `$XDG_CONFIG_HOME/slinit.d` (or `~/.config/slinit.d`), `/etc/slinit.d/user`, `/usr/lib/slinit.d/user`, `/usr/local/lib/slinit.d/user`

