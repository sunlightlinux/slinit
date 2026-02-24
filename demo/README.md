# slinit QEMU Demo

Reproducible QEMU environment for testing slinit as PID 1 with Alpine Linux.

## Quick Start

```bash
./build.sh    # Download Alpine, compile slinit, create initramfs
./run.sh      # Boot QEMU with slinit as init
```

## Requirements

- Go 1.25+
- `qemu-system-x86_64`
- `curl`, `cpio`, `gzip`
- KVM recommended (falls back to software emulation)

## Demo Services

| Service       | Type      | Description                              |
|---------------|-----------|------------------------------------------|
| boot          | internal  | Boot milestone (depends on system-init + tty) |
| system-init   | scripted  | Mounts /proc, /sys, /dev, /dev/pts       |
| tty           | process   | Interactive shell on console             |
| hello         | process   | Echo loop with log buffer                |
| ticker        | process   | Periodic timestamp output (alias: my-ticker) |
| trigger-test  | triggered | Externally triggered service             |
| dep-a         | internal  | Dependency chain leaf                    |
| dep-b         | internal  | Dependency chain middle (waits-for dep-a)|
| dep-chain     | internal  | Dependency chain root                    |
| restarter     | process   | Auto-restart on failure demo             |

## Supported Configuration Options

| Option                | Description                                      |
|-----------------------|--------------------------------------------------|
| `type`                | Service type (process, bgprocess, scripted, internal, triggered) |
| `command`             | Command to run                                   |
| `stop-command`        | Command to run on stop (scripted)                |
| `depends-on:`         | Hard dependency                                  |
| `waits-for:`          | Soft dependency (wait for start/fail)            |
| `before:`             | Ordering: start before target                    |
| `after:`              | Ordering: start after target                     |
| `provides`            | Alias name for service lookup                    |
| `restart`             | Auto-restart mode (yes, on-failure, no)          |
| `log-type`            | Output logging (buffer, file, none)              |
| `ready-notification`  | Readiness protocol (pipefd:N, pipevar:VARNAME)   |
| `socket-listen`       | Pre-opened Unix socket passed to child (fd 3)    |
| `socket-permissions`  | Socket file permissions                          |
| `socket-uid/gid`      | Socket file ownership                            |
| `options`             | Service flags (runs-on-console, etc.)            |
| `term-signal`         | Signal for graceful stop                         |

## Interactive Commands

Run these from the shell inside the VM:

```bash
# List all services
slinitctl list

# Boot timing analysis
slinitctl boot-time

# Service status
slinitctl status hello
slinitctl status restarter

# View log buffers
slinitctl catlog hello
slinitctl catlog ticker
slinitctl catlog restarter       # shows restart markers

# Trigger the triggered service
slinitctl trigger trigger-test
slinitctl list                   # trigger-test now shows [+]

# Service lifecycle
slinitctl stop ticker
slinitctl list
slinitctl start ticker
slinitctl restart hello

# Send signal
slinitctl signal HUP hello

# Reload config (modify /etc/slinit.d/hello, then:)
slinitctl reload hello

# Unload a stopped service from memory
slinitctl stop ticker
slinitctl unload ticker
slinitctl list                   # ticker gone

# Service aliases (provides) -- ticker has "provides = my-ticker"
slinitctl status my-ticker       # found by alias

# SysV init compatibility (alternative to slinitctl shutdown)
init 0                           # poweroff
init 6                           # reboot

# Clean shutdown (exits QEMU due to -no-reboot)
slinitctl shutdown reboot
```

## Dependency Graph

```
boot (internal)
├── depends-on: system-init (scripted)
├── depends-on: tty (process, console) ── depends-on: system-init
├── waits-for: hello (process, logbuf) ── depends-on: system-init
├── waits-for: ticker (process, logbuf) ── depends-on: system-init
├── waits-for: dep-chain (internal)
│   ├── depends-on: dep-b (internal) ── waits-for: dep-a ── depends-on: system-init
│   └── waits-for: restarter (process, restart) ── depends-on: system-init
└── waits-for: trigger-test (triggered) ── depends-on: system-init
```

## Exiting

- `init 0` -- orderly poweroff (SysV compat)
- `init 6` -- orderly reboot (SysV compat)
- `slinitctl shutdown reboot` -- orderly shutdown (QEMU exits with -no-reboot)
- `slinitctl shutdown poweroff` -- orderly poweroff
- `Ctrl+A, X` -- kill QEMU immediately

## Cleanup

```bash
./cleanup.sh          # Remove build artifacts (keeps download cache)
rm -rf _cache         # Remove cached downloads too
```
