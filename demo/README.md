# slinit QEMU Demo

Reproducible QEMU environment for testing slinit as PID 1 with Alpine Linux.

## Quick Start

```bash
./build.sh    # Download Alpine, compile slinit, create initramfs
./run.sh      # Boot QEMU with slinit as init
```

## Requirements

- Go 1.22+
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

# Check status via exit code (for scripting)
slinitctl is-started hello && echo "running"
slinitctl is-failed restarter || echo "not failed"

# View log buffers
slinitctl catlog hello
slinitctl catlog ticker
slinitctl catlog restarter       # shows restart markers

# Trigger / untrigger
slinitctl trigger trigger-test
slinitctl list                   # trigger-test now shows [+]
slinitctl untrigger trigger-test # reset trigger flag

# Service lifecycle
slinitctl stop ticker
slinitctl stop --force ticker    # force stop (even with dependents)
slinitctl list
slinitctl start ticker           # marks active (stays running)
slinitctl start --pin ticker     # start and pin in started state
slinitctl wake ticker             # start without marking active
slinitctl release ticker          # unmark active (stop if unrequired)
slinitctl restart hello
slinitctl unpin ticker            # remove start/stop pins

# Send signal to a service
slinitctl signal HUP hello
slinitctl signal TERM ticker
slinitctl signal USR1 hello

# Reload config (modify /etc/slinit.d/hello, then:)
slinitctl reload hello

# Unload a stopped service from memory
slinitctl stop ticker
slinitctl unload ticker
slinitctl list                   # ticker gone

# Service aliases (provides) -- ticker has "provides = my-ticker"
slinitctl status my-ticker       # found by alias

# Runtime environment management
slinitctl setenv hello KEY=VALUE
slinitctl unsetenv hello KEY
slinitctl getallenv hello

# Runtime dependency management
slinitctl add-dep hello depends-on system-init
slinitctl rm-dep hello waits-for dep-a

# Enable/disable (add/remove waits-for dep on boot or enable-via service)
slinitctl enable ticker
slinitctl enable --from boot ticker  # explicit source service
slinitctl disable ticker

# Offline enable/disable (no daemon needed)
slinitctl --offline enable ticker
slinitctl --offline -d /etc/slinit.d disable ticker

# Query dependents and loader info
slinitctl dependents boot
slinitctl query-load-mech

# SysV init compatibility (alternative to slinitctl shutdown)
init 0                           # poweroff  (sends SIGUSR2 to PID 1)
init 6                           # reboot    (sends SIGTERM to PID 1)

# Clean shutdown (exits QEMU due to -no-reboot)
slinitctl shutdown reboot
slinitctl shutdown poweroff
slinitctl shutdown halt
slinitctl shutdown softreboot      # restart slinit without kernel reboot

# Connect to system/user instance explicitly
slinitctl --system list
slinitctl --user list
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

## PID 1 Signal Handling

slinit handles SysV init signal conventions for compatibility with busybox and
other tools:

| Signal    | Action                | Source                        |
|-----------|-----------------------|-------------------------------|
| `SIGTERM` | reboot                | busybox `reboot`              |
| `SIGINT`  | reboot                | Ctrl-Alt-Del (via CAD)        |
| `SIGQUIT` | poweroff              | --                            |
| `SIGUSR1` | reopen control socket | recovery when fs writable     |
| `SIGUSR2` | poweroff              | busybox `poweroff`            |
| `SIGHUP`  | ignored               | --                            |

## Boot Failure Recovery

When running as PID 1 and all services stop without an explicit shutdown, slinit
detects a **boot failure**. The behavior depends on the `-r` flag:

- **Without `-r`**: an interactive prompt is shown on `/dev/console`:
  - `(r)eboot` -- reboot the system
  - `r(e)covery` -- start a `recovery` service (e.g. root shell)
  - `re(s)tart boot sequence` -- restart the boot service
  - `(p)ower off` -- power off the system
- **With `-r` / `--auto-recovery`**: automatically starts a `recovery` service.
  Falls back to reboot if the recovery service cannot be loaded.

To use auto-recovery in the demo, modify `run.sh` to pass `-r`:
```bash
slinit --system -r --services-dir /etc/slinit.d
```

To start multiple boot services, use `-t`:
```bash
slinit --system -t boot -t extra-service --services-dir /etc/slinit.d
```

## Exiting

- `init 0` -- orderly poweroff (sends SIGUSR2 to PID 1)
- `init 6` -- orderly reboot (sends SIGTERM to PID 1)
- `slinitctl shutdown reboot` -- orderly shutdown via control socket (QEMU exits with -no-reboot)
- `slinitctl shutdown poweroff` -- orderly poweroff via control socket
- `Ctrl+A, X` -- kill QEMU immediately

## Cleanup

```bash
./cleanup.sh          # Remove build artifacts (keeps download cache)
rm -rf _cache         # Remove cached downloads too
```
