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

| Service       | Type      | Description                                    |
|---------------|-----------|------------------------------------------------|
| boot          | internal  | Boot milestone (depends on system-init + tty)  |
| system-init   | scripted  | Mounts /proc, /sys, /dev, /dev/pts             |
| tty           | process   | Interactive shell on console                   |
| hello         | process   | Echo loop with log buffer                      |
| ticker        | process   | Periodic timestamp output (alias: my-ticker)   |
| trigger-test  | triggered | Externally triggered service                   |
| dep-a         | internal  | Dependency chain leaf                          |
| dep-b         | internal  | Dependency chain middle (waits-for dep-a)      |
| dep-chain     | internal  | Dependency chain root                          |
| restarter     | process   | Auto-restart on failure demo                   |
| cpu-pinned    | process   | CPU affinity demo (pinned to CPUs 0-1)         |
| hello-logged  | process   | Pipe logging producer (log-type=pipe)          |
| logger        | process   | Pipe logging consumer (consumer-of)            |
| env-demo      | process   | Environment variable substitution + env-file   |
| graceful-stop | process   | Stop-command demo (graceful cleanup on stop)    |
| recovery      | process   | Emergency shell after boot failure             |

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
slinitctl catlog cpu-pinned      # cpu-affinity service logs
slinitctl catlog logger          # pipe consumer output
slinitctl catlog env-demo        # env substitution output
slinitctl catlog graceful-stop

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

# Stop-command demo (graceful-stop runs cleanup before kill)
slinitctl stop graceful-stop
slinitctl catlog graceful-stop   # see stop-command output
slinitctl start graceful-stop

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

# Runtime environment management (per-service)
slinitctl setenv hello KEY=VALUE
slinitctl unsetenv hello KEY
slinitctl getallenv hello

# Global environment management
slinitctl setenv-global MY_VAR=test
slinitctl unsetenv-global MY_VAR
slinitctl getallenv-global

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
slinitctl query-name hello
slinitctl service-dirs

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

## slinit-check (Config Linter)

Offline and online validation of service configuration files.

```bash
# Offline: check service files from disk
slinit-check                          # checks "boot" in default dirs
slinit-check -d /etc/slinit.d hello ticker
slinit-check --system                 # check all system service dirs

# Online: query running daemon for service dirs and env, then check
slinit-check --online
slinit-check --online hello ticker
slinit-check --online -p /run/slinit.ctl   # explicit socket path
```

## slinit-monitor (Event Watcher)

Real-time service event monitoring with optional command execution.

```bash
# Watch all service events
slinit-monitor

# Watch and execute a command on each event (%n=name, %s=status, %v=event)
slinit-monitor -c 'echo "Service %n changed to %s (%v)"'

# Watch with initial state dump
slinit-monitor --initial

# Watch environment changes
slinit-monitor --env

# Exit after first event
slinit-monitor --exit
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
├── waits-for: trigger-test (triggered) ── depends-on: system-init
├── waits-for: cpu-pinned (process, cpu-affinity) ── depends-on: system-init
├── waits-for: hello-logged (process, pipe) ── depends-on: system-init
│   └── logger (process, consumer-of) ── depends-on: system-init
├── waits-for: env-demo (process, env-file) ── depends-on: system-init
└── waits-for: graceful-stop (process, stop-command) ── depends-on: system-init
```

## Feature Demos

### CPU Affinity
The `cpu-pinned` service demonstrates `cpu-affinity = 0-1` to pin a process
to specific CPU cores. Supported formats: `0 1 2`, `0-3`, `0,2,4`, `0-2 8-11`.

### Pipe Logging (consumer-of)
`hello-logged` produces output with `log-type = pipe`. The `logger` service
uses `consumer-of = hello-logged` to read that pipe and process the output.

### Environment Substitution
`env-demo` loads variables from `env-demo.env` via `env-file` and uses
`$VAR`, `${VAR:-default}`, and `${VAR:+alt}` substitution in its command.

### Stop-Command
`graceful-stop` demonstrates `stop-command`: when stopped, slinit runs the
stop-command first (allowing cleanup), then falls back to the termination
signal if the stop-command fails.

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
