# slinit

A service manager and init system inspired by [dinit](https://github.com/davmac314/dinit), written in Go.

slinit can run as PID 1 (init system) or as a user-level service manager. It uses a dinit-compatible configuration format and manages services with dependency tracking, automatic restart, and process lifecycle management.

## Features

- **Service types**: process, scripted, bgprocess, internal, triggered
- **Dependency management**: 6 dependency types (regular, waits-for, milestone, soft, before, after)
- **Process lifecycle**: SIGTERM with configurable timeout, SIGKILL escalation
- **Auto-restart**: configurable restart policy with rate limiting and smooth recovery
- **Dinit-compatible config**: key=value service description files
- **Control socket**: binary protocol over Unix domain socket for runtime management
- **slinitctl CLI**: list, start, stop, wake, release, restart, status, is-started, is-failed, trigger, untrigger, signal, reload, unload, catlog, setenv, unsetenv, getallenv, add-dep, rm-dep, enable, disable, shutdown, boot-time
- **Service aliases**: `provides` for alternative name lookup
- **Consumer pipes**: `consumer-of` to pipe output from one service into another
- **Log output**: buffer (in-memory, catlog), file (logfile with permissions/ownership), pipe (consumer-of)
- **Ready notification**: pipefd/pipevar readiness protocol for services
- **Socket activation**: pre-opened Unix listening socket passed to child (fd 3)
- **Hot reload**: reload service configuration from disk without restart
- **Service unload**: remove stopped services from memory
- **PID 1 init**: console setup, Ctrl+Alt+Del handling, child subreaper, orphan reaping
- **Process attributes**: nice, oom-score-adj, rlimits, ioprio, cgroup, no-new-privs, capabilities, securebits
- **Runtime environment**: setenv/unsetenv/getallenv via control socket, env-file loading
- **Runtime dependencies**: add-dep/rm-dep, enable/disable via control socket
- **SysV signal compat**: SIGTERM (reboot), SIGUSR1 (halt), SIGUSR2 (poweroff)
- **Shutdown**: orderly service stop, process cleanup (SIGTERM/SIGKILL), filesystem sync, reboot/halt/poweroff
- **Soft-reboot**: restart slinit without rebooting the kernel
- **Dual mode**: system init (PID 1) or user-level service manager

## Building

```bash
go build ./cmd/slinit
go build ./cmd/slinitctl
```

## Running

```bash
# User mode (default)
./slinit --services-dir /path/to/services

# System mode
./slinit --system --services-dir /etc/slinit.d
```

### Command-line options

| Flag | Description | Default |
|------|-------------|---------|
| `--services-dir` | Service description directory (comma-separated) | `~/.config/slinit.d` (user) or `/etc/slinit.d` (system) |
| `--socket-path` | Control socket path | `~/.slinitctl` or `/run/slinit.socket` |
| `--system` | Run as system service manager | `false` |
| `--user` | Run as user service manager | `true` |
| `--boot-service` | Name of the boot service to start | `boot` |
| `--log-level` | Log level (debug, info, notice, warn, error) | `info` |
| `--version` | Show version and exit | |

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
rlimit-nofile = 1024:4096
rlimit-core = unlimited
cgroup = /sys/fs/cgroup/workers
capabilities = cap_net_bind_service,cap_sys_nice
securebits = noroot keep-caps
options = no-new-privs
env-file = /etc/worker.env
run-as = worker:worker
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

### Configuration reference

| Option                    | Description                                      |
|---------------------------|--------------------------------------------------|
| `type`                    | Service type (process, bgprocess, scripted, internal, triggered) |
| `command`                 | Command to run                                   |
| `stop-command`            | Command to run on stop (scripted)                |
| `depends-on:`             | Hard dependency                                  |
| `depends-ms:`             | Milestone dependency (must start, then becomes soft) |
| `waits-for:`              | Soft dependency (wait for start/fail)            |
| `before:`                 | Ordering: start before target                    |
| `after:`                  | Ordering: start after target                     |
| `provides`                | Alias name for service lookup                    |
| `consumer-of:`            | Pipe output from named service into this one     |
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
| `socket-listen`           | Pre-opened Unix socket passed to child (fd 3)    |
| `socket-permissions`      | Socket file permissions                          |
| `socket-uid/gid`          | Socket file ownership                            |
| `pid-file`                | PID file path (bgprocess type)                   |
| `start-timeout`           | Timeout for service start (seconds)              |
| `stop-timeout`            | Timeout for service stop (seconds)               |
| `options`                 | Service flags (runs-on-console, no-new-privs, etc.) |
| `term-signal`             | Signal for graceful stop                         |
| `working-dir`             | Working directory for the process                |
| `run-as`                  | Run command as user:group                        |
| `env-file`                | Environment variables file (KEY=VALUE lines)     |
| `chain-to`                | Service to start after this one stops            |
| `nice`                    | Process scheduling priority (-20..19)            |
| `oom-score-adj`           | OOM killer score adjustment (-1000..1000)        |
| `ioprio`                  | I/O priority class:level (be:4, rt:0, idle)      |
| `cgroup`                  | Cgroup path for the child process                |
| `rlimit-nofile`           | File descriptor limit (soft:hard or unlimited)   |
| `rlimit-core`             | Core dump size limit (soft:hard or unlimited)    |
| `rlimit-data`             | Data segment size limit (soft:hard or unlimited) |
| `rlimit-as`               | Address space limit (soft:hard or unlimited)     |
| `capabilities`            | Ambient capabilities (cap_net_bind_service, etc.)|
| `securebits`              | Securebits flags (noroot, keep-caps, etc.)       |

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

## Control CLI (slinitctl)

`slinitctl` communicates with a running slinit instance via the control socket:

```bash
# List all loaded services
slinitctl list

# Start/stop/restart
slinitctl start myservice       # start and mark active
slinitctl wake myservice        # start without marking active
slinitctl stop myservice        # force stop
slinitctl release myservice     # unmark active (stop if unrequired)
slinitctl restart myservice

# Service status
slinitctl status myservice
slinitctl is-started myservice  # exit 0 if started, 1 otherwise
slinitctl is-failed myservice   # exit 0 if failed, 1 otherwise

# Trigger / untrigger
slinitctl trigger mytrigger
slinitctl untrigger mytrigger   # reset trigger state

# Send signal to a service process
slinitctl signal HUP myservice

# View buffered service output
slinitctl catlog myservice
slinitctl catlog --clear myservice

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

# Enable/disable (add/remove waits-for dep on boot service)
slinitctl enable myservice
slinitctl disable myservice

# Boot timing analysis
slinitctl boot-time

# Initiate system shutdown
slinitctl shutdown poweroff
slinitctl shutdown reboot

# Use a custom socket path
slinitctl --socket-path /tmp/test.socket list
```

## Architecture

slinit follows Go-idiomatic patterns while preserving dinit's proven service management design:

- **Goroutines + channels** replace dinit's dasynq event loop
- **Interface + struct embedding** replaces C++ virtual method dispatch
- **Two-phase state transitions** (propagation + execution) preserve correctness from dinit
- **One goroutine per child process** for monitoring, with channel-based notification
- **Binary control protocol** over Unix domain sockets, goroutine-per-connection
- **PID 1 shutdown sequence**: emergency timeout, process cleanup, filesystem sync, reboot syscalls

## Project structure

```
slinit/
├── cmd/
│   ├── slinit/          # Daemon entry point
│   └── slinitctl/       # Control CLI
├── pkg/
│   ├── service/         # Service types, state machine, dependency graph
│   ├── config/          # Dinit-compatible config parser and loader
│   ├── control/         # Control socket protocol and server
│   ├── shutdown/        # PID 1 init, shutdown executor, soft-reboot
│   ├── process/         # Process execution and monitoring
│   ├── eventloop/       # Event loop, signals, timers
│   └── logging/         # Console logger
├── internal/util/       # Path and parsing utilities
└── demo/                # QEMU test environment
```

## Testing

```bash
go test ./...
# 183 tests across 5 packages
```

## Roadmap

- [x] **Phase 1**: Foundation -- types, state machine, config parser, event loop
- [x] **Phase 2**: Process services -- fork/exec, child monitoring, restart logic
- [x] **Phase 3**: Full dependency graph -- all 6 dep types, TriggeredService, BGProcessService
- [x] **Phase 4**: Control protocol + `slinitctl` CLI
- [x] **Phase 5**: PID 1 mode + shutdown sequence
- [x] **Phase 6**: Advanced features
  - [x] catlog (buffered output retrieval)
  - [x] reload (hot config reload)
  - [x] ready-notification (pipefd/pipevar)
  - [x] socket activation
  - [x] provides (service aliases)
  - [x] unload (remove stopped services)
  - [x] consumer-of (pipe between services)
  - [x] log-type = file (logfile with permissions/ownership)
  - [x] untrigger (reset trigger state)
  - [x] wake (start without marking active)
  - [x] release (unmark active, conditional stop)
  - [x] is-started / is-failed (exit code status check)
  - [x] setenv / unsetenv / getallenv (runtime environment management)
  - [x] add-dep / rm-dep (runtime dependency management)
  - [x] enable / disable (boot service integration)
  - [x] nice, oom-score-adj, ioprio, cgroup, rlimits, no-new-privs
  - [x] capabilities (ambient caps via SysProcAttr) + securebits

## License

[Apache License 2.0](LICENSE)
