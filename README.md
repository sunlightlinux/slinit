# slinit

A service manager and init system inspired by [dinit](https://github.com/davmac314/dinit), written in Go.

slinit can run as PID 1 (init system) or as a user-level service manager. It uses a dinit-compatible configuration format and manages services with dependency tracking, automatic restart, and process lifecycle management.

## Features

- **Service types**: internal, process (long-running), scripted (start/stop commands)
- **Dependency management**: 6 dependency types (regular, soft/waits-for, milestone, before, after)
- **Process lifecycle**: SIGTERM with configurable timeout, SIGKILL escalation
- **Auto-restart**: configurable restart policy with rate limiting and smooth recovery
- **Dinit-compatible config**: key=value service description files
- **Dual mode**: system init (PID 1) or user-level service manager

## Building

```bash
go build ./cmd/slinit
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

### Service types

| Type | Description |
|------|-------------|
| `process` | Long-running daemon managed by slinit |
| `scripted` | Service controlled by start/stop commands |
| `internal` | Milestone service with no associated process |
| `bgprocess` | Background process (planned) |
| `triggered` | Triggered service (planned) |

### Dependency types

| Directive | Description |
|-----------|-------------|
| `depends-on` | Hard dependency - start required, stop propagates |
| `depends-ms` | Milestone dependency - like depends-on for milestones |
| `waits-for` | Soft dependency - waits for start, but failure doesn't propagate |
| `before` | Ordering - this service starts before the named service |
| `after` | Ordering - this service starts after the named service |

## Architecture

slinit follows Go-idiomatic patterns while preserving dinit's proven service management design:

- **Goroutines + channels** replace dinit's dasynq event loop
- **Interface + struct embedding** replaces C++ virtual method dispatch
- **Two-phase state transitions** (propagation + execution) preserve correctness from dinit
- **One goroutine per child process** for monitoring, with channel-based notification

## Project structure

```
slinit/
├── cmd/slinit/          # Daemon entry point
├── pkg/
│   ├── service/         # Service types, state machine, dependency graph
│   ├── config/          # Dinit-compatible config parser and loader
│   ├── process/         # Process execution and monitoring
│   ├── eventloop/       # Event loop, signals, timers
│   └── logging/         # Console logger
└── internal/util/       # Path and parsing utilities
```

## Roadmap

- [x] **Phase 1**: Foundation - types, state machine, config parser, event loop
- [x] **Phase 2**: Process services - fork/exec, child monitoring, restart logic
- [ ] **Phase 3**: Full dependency graph - all 6 dep types, triggered/bgprocess services
- [ ] **Phase 4**: Control protocol + `slinitctl` CLI
- [ ] **Phase 5**: PID 1 mode + shutdown sequence
- [ ] **Phase 6**: Advanced features - socket activation, cgroups, rlimits

## Testing

```bash
go test ./...
```

## License

[Apache License 2.0](LICENSE)
