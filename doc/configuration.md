# slinit configuration reference

This document covers service description files and the daemon's
command line: worked examples organised by service shape, followed by
reference tables grouped by topic.

| Reference | Covers |
|---|---|
| [slinit-service(5)](man/slinit-service.5.md) | Every service directive — **authoritative** |
| [slinit(8)](man/slinit.8.md) | Every daemon flag, PID 1 behaviour, kernel command line — **authoritative** |
| [features.md](features.md) | Generated list of every name the parser and control protocol accept |
| [operators-guide.md](operators-guide.md) | Task-oriented guide: systemd mappings, first service, troubleshooting |

Where this document and a man page disagree, the man page is correct
and this document has a bug.

## Contents

- [File format](#file-format)
- [Examples by service shape](#examples-by-service-shape)
- [Directive reference](#directive-reference)
- [Service types](#service-types)
- [Environment variable substitution](#environment-variable-substitution)
- [Appendix: daemon command-line options](#appendix-daemon-command-line-options)

## File format

A service description is a single file whose name is the service name —
no extension and no `[Section]` headers. Each line holds one setting:

```ini
# Comments start with '#'
key = value          # scalar setting
command += --verbose # append to a list-valued setting
depends-on: network  # dependency keys also accept ':' (dinit convention)
```

Files are searched in the service directories listed under
[Default service directories](#default-service-directories); the first
match wins.

## Examples by service shape

### Long-running process

The common case: a program that stays in the foreground, supervised
directly by slinit.

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

### Self-backgrounding daemon

For a program that forks and lets its parent exit. slinit learns the
daemon's PID from the pidfile.

```ini
# /etc/slinit.d/mydaemon
type = bgprocess
command = /usr/sbin/mydaemon
pid-file = /run/mydaemon.pid
stop-timeout = 15
depends-on: network
```

### Output to a log file

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

### Process attributes, limits and capabilities

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
options = no-new-privs
env-file = /etc/worker.env
run-as = worker:worker
```

### runit-style supervision

Finish scripts, polled readiness, a pre-stop hook, a custom signal
handler, and log rotation with filtering.

```ini
# /etc/slinit.d/webapp
type = process
command = /usr/bin/webapp
finish-command = /usr/local/bin/cleanup.sh
ready-check-command = /usr/bin/curl -sf http://localhost:8080/health
ready-check-interval = 500ms
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

### Consumer pipe

The consumer reads the producer's standard output on its standard input.

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

### Service template

A description whose filename ends in `@` is a template. Each instance
is started as `name@argument`, and `$1` (or `${1}`) expands to the
argument.

```ini
# /etc/slinit.d/myservice@
type = process
command = /usr/bin/myservice --instance $1
working-dir = /var/lib/myservice/${1}
depends-on: network
```

```bash
slinitctl start myservice@web     # $1 expands to "web"
```

### Shared logger

Several services feed a single logger process. Each line the logger
receives is prefixed with its producer's name: `[app-one] …`,
`[app-two] …`.

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

### Virtual TTY

A screen-like terminal that can be attached and detached at runtime.

```ini
# /etc/slinit.d/interactive-svc
type = process
command = /usr/bin/myapp
vtty = true
vtty-scrollback = 131072
```

```bash
slinitctl attach interactive-svc  # Ctrl+] detaches
```

### Periodic task

A command run on a schedule for as long as the parent service is
running.

```ini
# /etc/slinit.d/worker
type = process
command = /usr/bin/worker
cron-command = /usr/bin/cleanup-temp
cron-interval = 3600
cron-delay = 60
cron-on-error = continue
```

### Custom enable target

By default `slinitctl enable` adds a `waits-for` edge from `boot`.
`@meta enable-via` names a different service to hang the edge from.

```ini
# /etc/slinit.d/optional-svc
type = process
command = /usr/bin/optional
@meta enable-via mygroup
```

```bash
slinitctl enable optional-svc     # adds the edge mygroup → optional-svc
```

## Directive reference

The tables below group the most commonly used directives by topic. They
are a summary: [slinit-service(5)](man/slinit-service.5.md) documents
every directive, including those not listed here, with full value
syntax and the release that introduced it.

**Value conventions.** Time values come in two forms, and each
directive accepts the form shown in its row:

| Notation | Syntax | Examples |
|---|---|---|
| *seconds* | Decimal number of seconds | `10`, `0.5` |
| *Go duration* | Number with a unit suffix | `500ms`, `30s`, `1h30m` |

*bool* values are `yes` / `no`; `true` / `false` and `1` / `0` are also
accepted. List-valued settings accept `+=` to append.

### Core

| Directive | Value | Description |
|---|---|---|
| `type` | `process` \| `bgprocess` \| `scripted` \| `internal` \| `triggered` | Service type — see [Service types](#service-types) |
| `command` | program [args…] | Command to run; supports `+=` |
| `stop-command` | program [args…] | The stop script (`scripted`); for `process` / `bgprocess`, run instead of sending the stop signal; supports `+=` |
| `working-dir` | path | Working directory for the process |
| `run-as` | user[:group] | Run the command as this user and group |
| `provides` | name | Alias under which the service can also be looked up |
| `options` | flag… | Service flags: `runs-on-console`, `unmask-intr`, `no-new-privs`, … |
| `load-options` | flag… | Loader flags: `export-passwd-vars`, `export-service-name` |
| `@include` | path | Include another file; error if it does not exist |
| `@include-opt` | path | Include another file; ignored if it does not exist |
| `@meta enable-via` | service | Default source service for `enable` / `disable` |

### Dependencies

| Directive | Value | Description |
|---|---|---|
| `depends-on:` | service | Hard dependency — must start; stopping it stops this service |
| `depends-ms:` | service | Milestone dependency — must start, then behaves as a soft dependency |
| `waits-for:` | service | Soft dependency — waits for it to start or fail; failure does not propagate |
| `prepared-by:` | service | Hard dependency that is also restarted every time this service restarts |
| `before:` | service | Ordering only — this service starts before the named one |
| `after:` | service | Ordering only — this service starts after the named one |
| `chain-to` | service | Service to start once this one has stopped |
| `consumer-of` | service | Read the named service's output on stdin (`=` or `:`) |
| `bundle-of` | service, … | s6-rc-style group: an `internal` service that brings the named services up as one unit; comma- or space-separated, or repeated |

### Lifecycle and restart

| Directive | Value | Description |
|---|---|---|
| `restart` | `yes` \| `no` \| `on-failure` | Automatic restart policy |
| `restart-delay` | seconds | Delay before a restart |
| `restart-limit-count` | integer | Maximum restarts within `restart-limit-interval` |
| `restart-limit-interval` | seconds | Window over which restarts are counted |
| `start-timeout` | seconds | Time allowed to reach *started* |
| `stop-timeout` | seconds | Time between `term-signal` and SIGKILL |
| `term-signal` | signal | Signal used for a graceful stop |
| `runtime-max-sec` | Go duration | Hard cap on time spent *started*; the service is stopped when it is reached |
| `pre-start-command` | program [args…] | Runs before `command`; synchronous, a non-zero exit fails the start |
| `post-start-command` | program [args…] | Runs right after the fork, before readiness is confirmed; asynchronous, result only logged |
| `finish-command` | program [args…] | Runs after the process exits, before any restart |
| `pre-stop-hook` | program [args…] | Runs before SIGTERM; receives the PID as an argument |
| `control-command-SIG` | program [args…] | Run in place of sending SIG — by `slinitctl signal`, a stop (`TERM`) or pause/continue (`STOP`/`CONT`), e.g. `control-command-HUP` |
| `oom-policy` | `continue` \| `stop` \| `kill` | Reaction to a cgroup v2 OOM kill |
| `failure-action` | `none` \| `reboot` \| `poweroff` \| `halt` \| `exit` | System action when the service fails permanently |
| `success-action` | `none` \| `reboot` \| `poweroff` \| `halt` \| `exit` | System action when the service finishes cleanly |
| `reboot-argument` | string | Argument passed to the reboot system call |

### Readiness and activation

| Directive | Value | Description |
|---|---|---|
| `ready-notification` | `pipefd:N` \| `pipevar:VAR` | Readiness protocol |
| `ready-check-command` | program [args…] | Polled readiness check, an alternative to `ready-notification` |
| `ready-check-interval` | Go duration | Polling interval for `ready-check-command` (default `100ms`) |
| `pid-file` | path | PID file written by a `bgprocess` daemon |
| `socket-listen` | path \| `tcp:host:port` \| `udp:host:port` | Listening socket passed to the child via `LISTEN_FDS`; repeat or `+=` for several |
| `socket-activation` | `immediate` \| `on-demand` | The socket opens when the service starts; `on-demand` is accepted but not implemented and behaves like `immediate` |
| `socket-reuseport` | bool | Set `SO_REUSEPORT` on `tcp:` / `udp:` listeners so several instances can share a port — see the [operator's guide](operators-guide.md#scaling-a-hot-port-across-workers) |
| `socket-permissions` | octal | Mode of a Unix socket file |
| `socket-uid`, `socket-gid` | integer | Ownership of a Unix socket file |
| `file-descriptor-store-max` | integer | Enable the `sd_notify` `FDSTORE=1` file-descriptor store across restarts |
| `condition-*`, `assert-*` | predicate | systemd-style start predicates (13 kinds, `!` negates); a failed condition skips the start silently, a failed assertion fails it |

### Environment and process I/O

| Directive | Value | Description |
|---|---|---|
| `env-file` | path | `KEY=VALUE` file; supports the `!clear`, `!unset` and `!import` meta-commands |
| `env-dir` | directory | runit-style environment directory, one file per variable |
| `chroot` | path | `chroot(2)` into this directory before exec |
| `new-session` | bool | Start the process in a new session (`setsid`) |
| `lock-file` | path | Exclusive `flock` held while running, preventing duplicate instances |
| `close-stdin`, `close-stdout`, `close-stderr` | bool | Redirect the stream to `/dev/null` |
| `vtty` | bool | Allocate a virtual TTY for attach / detach |
| `vtty-scrollback` | bytes | Virtual TTY scrollback buffer (default 64 KiB) |
| `inittab-id`, `inittab-line` | string | UTMPX inittab ID and line for session tracking |

### Logging

| Directive | Value | Description |
|---|---|---|
| `log-type` | `none` \| `buffer` \| `file` \| `pipe` \| `command` | Where the service's output goes |
| `log-buffer-size` | bytes | In-memory buffer size (`log-type = buffer`) |
| `logfile` | path | Log file path (`log-type = file`) |
| `logfile-permissions` | octal | Log file mode (default `0600`) |
| `logfile-uid`, `logfile-gid` | integer | Log file ownership |
| `logfile-max-size` | bytes | Rotate when the file reaches this size |
| `logfile-max-files` | integer | Number of rotated files to keep |
| `logfile-rotate-time` | seconds | Rotate at this interval |
| `log-processor` | program [args…] | Command run on each rotated file |
| `log-include` | regex | Write only matching lines |
| `log-exclude` | regex | Drop matching lines |
| `log-select` | `+regex` `-regex` … | s6-log-style selection chain; the last match decides; exclusive with `log-include` / `log-exclude` |
| `log-rate-limit-interval` | Go duration | Token-bucket window; excess lines are dropped |
| `log-rate-limit-burst` | integer | Lines allowed per window |
| `log-level-max` | `emerg` … `debug` | Drop lines above this syslog severity |
| `shared-logger` | service | Send output to a shared logger, prefixed with this service's name |

### Resources and scheduling

| Directive | Value | Description |
|---|---|---|
| `nice` | −20 … 19 | Scheduling priority |
| `oom-score-adj` | −1000 … 1000 | OOM-killer score adjustment |
| `ioprio` | `be:N` \| `rt:N` \| `idle` | I/O scheduling class and level |
| `cpu-affinity` | list | CPU set, e.g. `0-3`, `0 1 2`, `0,2,4` |
| `cgroup` | path | cgroup for the child process, absolute and under `/sys/fs/cgroup` (alias `run-in-cgroup`, dinit) |
| `slice` | name | Hierarchical cgroup parent, systemd-style |
| `delegate` | bool \| controller… | Delegate the cgroup subtree to the service |
| `rlimit-nofile` | soft[:hard] \| `unlimited` | Open file descriptors |
| `rlimit-core` | soft[:hard] \| `unlimited` | Core dump size |
| `rlimit-data` | soft[:hard] \| `unlimited` | Data segment size |
| `rlimit-as` | soft[:hard] \| `unlimited` | Address space (alias `rlimit-addrspace`, dinit) |

### Identity, privileges and credentials

| Directive | Value | Description |
|---|---|---|
| `capabilities` | cap,… | Ambient capabilities, e.g. `cap_net_bind_service` |
| `securebits` | bit… | Securebits flags, e.g. `noroot keep-caps`; set by `slinit-runner` before exec |
| `dynamic-user` | bool | Allocate a transient UID/GID for each start; released when the service stops |
| `load-credential` | `NAME:PATH` | Copy a file into `/run/credentials/<svc>/` |
| `set-credential` | `NAME:VALUE` | Write an inline value as a credential |
| `runtime-directory` | name… | Managed `/run/<name>`, owned by `run-as` |
| `state-directory` | name… | Persistent `/var/lib/<name>`; also `cache-`, `logs-` and `configuration-directory` |

### Filesystem sandbox

Applied in a private mount namespace by `slinit-runner`, which must be
installed for any service that uses these directives.

| Directive | Value | Description |
|---|---|---|
| `private-tmp` | bool | Private tmpfs on `/tmp` and `/var/tmp` |
| `protect-system` | `no` \| `yes` \| `full` \| `strict` | Mount `/usr`, `/boot`, `/efi` (and more at higher levels) read-only |
| `protect-home` | `no` \| `yes` \| `read-only` \| `tmpfs` | Restrict `/home`, `/root`, `/run/user` |
| `read-only-paths` | path… | Bind read-only |
| `read-write-paths` | path… | Bind read-write; punches holes through `protect-system` |
| `inaccessible-paths` | path… | Hide behind an empty mount |
| `bind-paths` | src[:dst]… | Bind host paths into the sandbox, read-write |
| `bind-read-only-paths` | src[:dst]… | Bind host paths into the sandbox, read-only |
| `temporary-filesystem` | path[:options]… | Mount a fresh tmpfs |
| `protect-proc` | `default` \| `noaccess` \| `invisible` \| `ptraceable` | `/proc` `hidepid` mode |
| `proc-subset` | `all` \| `pid` | Subset of `/proc` that is visible |

### System-call filtering and kernel protection

Also enforced by `slinit-runner`; every setting fails closed.

| Directive | Value | Description |
|---|---|---|
| `system-call-filter` | item… | seccomp allow list; `@group` names a curated set, a leading `~` makes it a deny list |
| `system-call-architectures` | name… | Architectures allowed to make system calls |
| `system-call-error-number` | errno \| `kill` \| `log` \| `trap` | Result of a filtered call (default `EPERM`) |
| `protect-kernel-tunables` | bool | Read-only `/proc/sys` and `/sys` |
| `protect-kernel-modules` | bool | Block `init_module`, `finit_module`, `delete_module` |
| `protect-kernel-logs` | bool | Block `syslog` |
| `protect-clock` | bool | Block `clock_settime`, `settimeofday`, `adjtimex` |
| `protect-control-groups` | bool | Read-only `/sys/fs/cgroup` |
| `protect-hostname` | bool | Block `sethostname`, `setdomainname` |
| `lock-personality` | bool | Block `personality` |

### Periodic tasks

| Directive | Value | Description |
|---|---|---|
| `cron-command` | program [args…] | Command run periodically while the service is running (`process` services only) |
| `cron-interval` | seconds or Go duration | Interval between runs |
| `cron-delay` | seconds or Go duration | Delay before the first run |
| `cron-on-error` | `continue` \| `stop` | Behaviour when the command fails (default `continue`) |
| `cron-calendar` | expression | systemd `OnCalendar=` syntax: `daily`, `Mon..Fri 09:00`, `*:0/15` |
| `cron-randomized-delay` | Go duration | Upper bound of random jitter added to each run, interval and calendar modes alike |
| `cron-fixed-random-delay` | bool | Derive the jitter once from the machine ID instead of per run (systemd `FixedRandomDelay=`) |
| `cron-persistent` | bool | Catch-up run when a scheduled run was missed; the last-run time is kept on disk, so it survives a reboot |

## Service types

| Type | Use it for | Notes |
|---|---|---|
| `process` | A long-running program that stays in the foreground | slinit supervises the PID it forked |
| `bgprocess` | A daemon that forks and lets its parent exit | Requires `pid-file`; monitored by polling |
| `scripted` | A job that runs to completion | `command` starts it, the optional `stop-command` stops it |
| `internal` | A milestone or grouping name | No process |
| `triggered` | A service that waits for an external trigger | Completes its start on `slinitctl trigger` |

## Environment variable substitution

Values are expanded at load time:

| Syntax | Expands to |
|---|---|
| `$VAR`, `${VAR}` | The value of `VAR` |
| `${VAR:-default}` | `default` if `VAR` is unset or empty |
| `${VAR:+alt}` | `alt` if `VAR` is set and non-empty, otherwise nothing |
| `$$` | A literal `$` |
| `$/VAR` | The value of `VAR`, split on whitespace into separate arguments |
| `$1`, `${1}` | The template argument of a `name@argument` service |

## Appendix: daemon command-line options

A summary of every option, grouped by topic;
[slinit(8)](man/slinit.8.md) is authoritative.

### Mode and instance

| Flag | Description | Default |
|---|---|---|
| `-s`, `--system` | Run as the system service manager | |
| `-m`, `--system-mgr` | Run as the system manager even when not PID 1 | |
| `-u`, `--user` | Run as a per-user service manager | default mode |
| `-o`, `--container` | Container mode (Docker, LXC, Podman) | `false` |
| `-S`, `--sys` | Override platform detection — see [below](#platform-names) | auto |
| `-d`, `--services-dir` | Service directories, comma-separated | see [below](#default-service-directories) |
| `--conf-dir` | `conf.d` overlay directories, comma-separated; `none` disables overlays | |
| `-p`, `--socket-path` | Control socket path | `~/.slinitctl` (user), `/run/slinit.socket` (system) |
| `--version` | Print the version and exit | |

### Boot

| Flag | Description | Default |
|---|---|---|
| `-t`, `--service` | Service to start at boot; repeatable, positional arguments also accepted | `boot` |
| `-r`, `--auto-recovery` | Start the `recovery` service on boot failure (PID 1) | `false` |
| `-e`, `--env-file` | Environment file loaded at startup | |
| `-F`, `--ready-fd` | File descriptor notified when the boot service is ready | `-1` |
| `-W`, `--wait-fd` | Block until EOF on this descriptor before booting (container entrypoint sync) | `-1` |
| `--active-profile` | Activate a named profile; only services declaring `profile = <name>` (or none) are eligible | |
| `--watch-services-dir` | Load and unload services as files appear and disappear in the service directories (inotify) | `false` |
| `--sentinel-dir` | Watch for runit-compatible sentinel files (`stopit`, `reboot`, `poweroff`) | |
| `--banner` | Boot banner printed to the console; empty disables it | `slinit booting...` |
| `--umask` | Initial umask, octal | `0022` |
| `--run-mode` | How `/run` is staged: `mount` (fresh tmpfs), `remount` (unmount and mount), `keep` (untouched) | `mount` |
| `--devtmpfs-path` | Mount devtmpfs here; empty disables | `/dev` |
| `--kcmdline-dest` | Snapshot `/proc/cmdline` here; empty disables | `/run/slinit/kcmdline` |
| `--kernel-env-store` | Extract `KEY=VALUE` tokens from the kernel command line into this env-file, for services to load with `env-file`; empty disables | |
| `--restore-from-snapshot` | Replay an operator-intent snapshot after a soft reboot | |
| `--persist-intent` | Directory where pin transitions are persisted, so `stop --pin X` survives a reboot; empty disables. Recommended: `/var/lib/slinit/intent` | |

### Scheduling and resources

| Flag | Description | Default |
|---|---|---|
| `--parallel-start-limit` | Maximum concurrent service starts; `0` is unlimited | `0` |
| `--parallel-start-slow-threshold` | Time after which a starting service counts as slow | `10s` |
| `-b`, `--cgroup-path` | Default cgroup base path for services | |
| `-a`, `--cpu-affinity` | Default CPU affinity for the daemon and services, e.g. `0-3` | |
| `--rlimits` | Global rlimits inherited by services: `name=soft[:hard]`, comma-separated | |

### Shutdown

| Flag | Description | Default |
|---|---|---|
| `--shutdown-grace` | SIGTERM → SIGKILL grace period during shutdown | `3s` |
| `--emergency-timeout` | How long to wait for services to drain before the forced path: SIGKILL stragglers, log the services that blocked, issue the reboot call. Raise it for large stop cascades | `90s` |
| `--shutdown-final-sleep` | Settle pause between the final SIGKILL and unmounting, e.g. `500ms` | `0` |
| `--minimum-uptime-sec` | Anti-boot-loop floor: delay shutdown or reboot until the system has been up this long | `0` |
| `--no-wall` | Do not broadcast wall messages at shutdown | `false` |

### Logging

| Flag | Description | Default |
|---|---|---|
| `--log-level` | `debug`, `info`, `notice`, `warn` or `error` | `info` |
| `--console-level` | Minimum level shown on the console | `--log-level` |
| `-q`, `--quiet` | Show errors only | `false` |
| `-l`, `--log-file` | Log to a file instead of the console | |
| `-1`, `--console-dup` | Also copy log output to `/dev/console` when `--log-file` is set | `false` |
| `--catch-all-log` | Early-boot catch-all log path | `/run/slinit/catch-all.log` |
| `-B`, `--no-catch-all` | Disable the catch-all logger | `false` |
| `--timestamp-format` | `wallclock`, `iso`, `tai64n` or `none` | `wallclock` |
| `--stderr-ring-buffer-size` | Keep the daemon's recent log lines in a ring buffer of this many bytes, re-emitted periodically | `0` (off) |
| `--stderr-ring-buffer-interval` | How often the ring buffer is re-emitted | `15m` |

### Observability

| Flag | Description | Default |
|---|---|---|
| `--metrics-listen` | Serve Prometheus metrics on `host:port` or `unix:/path` | off |
| `--heartbeat-interval` | Log a one-line health summary at this interval | `0` (off) |
| `--heartbeat-restart-window` | Window over which the heartbeat's restart count is computed | `1m` |

### Hardware watchdog

| Flag | Description | Default |
|---|---|---|
| `--watchdog-device` | Watchdog device to feed (PID 1 and container mode) | `/dev/watchdog0`, then `/dev/watchdog` |
| `--watchdog-timeout` | Kernel-side timeout (`WDIOC_SETTIMEOUT`) | `60s` |
| `--watchdog-interval` | How often the device is pinged | timeout ÷ 3 |
| `--no-watchdog` | Do not feed the watchdog even as PID 1 | `false` |

### Default service directories

Used when `--services-dir` is not given, searched in this order:

| Mode | Directories |
|---|---|
| System | `/etc/slinit.d`, `/run/slinit.d`, `/usr/local/lib/slinit.d`, `/lib/slinit.d` |
| User | `$XDG_CONFIG_HOME/slinit.d` (or `~/.config/slinit.d`), `/etc/slinit.d/user`, `/usr/lib/slinit.d/user`, `/usr/local/lib/slinit.d/user` |

### Platform names

Accepted by `-S` / `--sys`:

| Kind | Names |
|---|---|
| Containers | `docker`, `lxc`, `podman`, `systemd-nspawn`, `openvz`, `vserver`, `rkt`, `wsl` |
| Hypervisors | `kvm`, `qemu`, `vmware`, `microsoft` (Hyper-V), `oracle` (VirtualBox), `bochs`, `xen0`, `xenu`, `uml` |
| None | `none` — bare metal, no platform-specific behaviour |
