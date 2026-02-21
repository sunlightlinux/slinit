# slinit

A service manager and init system inspired by [dinit](https://github.com/davmac314/dinit), written in Go.

slinit can run as PID 1 (init system) or as a user-level service manager. It uses a dinit-compatible configuration format and manages services with dependency tracking, automatic restart, and process lifecycle management.

## Features

- **Service types**: internal, process, scripted, bgprocess (self-backgrounding daemons), triggered (externally triggered)
- **Dependency management**: 6 dependency types (regular, soft/waits-for, milestone, before, after)
- **Process lifecycle**: SIGTERM with configurable timeout, SIGKILL escalation
- **Auto-restart**: configurable restart policy with rate limiting and smooth recovery
- **Dinit-compatible config**: key=value service description files
- **Control socket**: binary protocol over Unix domain socket for runtime management
- **slinitctl CLI**: list, start, stop, restart, status, shutdown, trigger, signal
- **PID 1 init**: console setup, Ctrl+Alt+Del disable, child subreaper, orphan reaping
- **Shutdown**: orderly process cleanup (SIGTERM/SIGKILL), filesystem sync, reboot/halt/poweroff syscalls
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
restart = true
smooth-recovery = true
depends-on: network
waits-for: logging
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
| `depends-on` | Hard dependency - start required, stop propagates |
| `depends-ms` | Milestone dependency - like depends-on for milestones |
| `waits-for` | Soft dependency - waits for start, but failure doesn't propagate |
| `before` | Ordering - this service starts before the named service |
| `after` | Ordering - this service starts after the named service |

## Control CLI (slinitctl)

`slinitctl` communicates with a running slinit instance via the control socket:

```bash
# List all loaded services
slinitctl list

# Start/stop/restart a service
slinitctl start myservice
slinitctl stop myservice
slinitctl restart myservice

# Show detailed service status
slinitctl status myservice

# Trigger a triggered service
slinitctl trigger mytrigger

# Send signal to a service process
slinitctl signal HUP myservice

# Initiate system shutdown
slinitctl shutdown poweroff

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
└── internal/util/       # Path and parsing utilities
```

## Roadmap

- [x] **Phase 1**: Foundation - types, state machine, config parser, event loop
- [x] **Phase 2**: Process services - fork/exec, child monitoring, restart logic
- [x] **Phase 3**: Full dependency graph - all 6 dep types validated, TriggeredService, BGProcessService
- [x] **Phase 4**: Control protocol + `slinitctl` CLI
- [x] **Phase 5**: PID 1 mode + shutdown sequence
- [ ] **Phase 6**: Advanced features - socket activation, cgroups, rlimits

## Testing

```bash
go test ./...
```

## License

[Apache License 2.0](LICENSE)
