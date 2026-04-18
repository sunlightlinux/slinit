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
| cron-demo     | process   | cron-command periodic task (fires every 10s)   |
| shared-log    | process   | SharedLogMux central logger (writes /run/shared-log.txt) |
| app-one       | process   | Producer into shared-log (shared-logger)       |
| app-two       | process   | Producer into shared-log (shared-logger)       |
| runit-svc     | process   | Runit-feature bundle: ready-check + pre-stop-hook + control-command + env-dir + lock-file + finish-command |
| vtty-svc      | process   | Virtual TTY demo (`slinitctl attach vtty-svc`) |
| hello-initd   | scripted  | `/etc/init.d/hello-initd` auto-detected via LSB headers, sources `/etc/rc.conf` + `/etc/conf.d/hello-initd` |
| slinit-mount  | process   | Autofs lazy-mount daemon                       |
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

# Pause/continue (SIGSTOP/SIGCONT)
slinitctl pause hello
slinitctl continue hello

# Start once (no auto-restart)
slinitctl once hello

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
init default                     # start runlevel-default service
init single                      # start runlevel-single service (recovery)

# SysV compat via argv[0] dispatch (symlinks → slinit binary)
halt                             # invokes slinit → ShutdownHalt
poweroff                         # invokes slinit → ShutdownPoweroff
reboot                           # invokes slinit → ShutdownReboot

# OpenRC compat shims (argv-translate over slinitctl)
rc-service hello status          # → slinitctl status hello
rc-update add hello default      # → slinitctl --from runlevel-default enable hello
rc-status default                # → slinitctl graph runlevel-default

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

## slinit-shutdown / slinit-nuke

Standalone shutdown helpers for emergency or scripted use:

```bash
slinit-shutdown -r               # orderly reboot (via control socket)
slinit-shutdown -p               # orderly poweroff
slinit-shutdown -h               # orderly halt
slinit-shutdown -s               # soft reboot (restart slinit, keep kernel)
slinit-shutdown -k               # kexec reboot (requires preloaded kernel)

# Emergency userspace cleanup (TERM → grace → KILL)
slinit-nuke                      # default 2s grace
slinit-nuke --grace 500ms
slinit-nuke -9                   # skip TERM, SIGKILL immediately
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
├── waits-for: graceful-stop (process, stop-command) ── depends-on: system-init
├── waits-for: cron-demo (process, cron-command) ── depends-on: system-init
├── waits-for: shared-log (process, log-type=pipe) ── depends-on: system-init
│   ├── app-one (shared-logger) ── depends-on: shared-log
│   └── app-two (shared-logger) ── depends-on: shared-log
├── waits-for: runit-svc (process, ready-check + hooks + env-dir + lock) ── depends-on: system-init
└── waits-for: vtty-svc (process, vtty=true) ── depends-on: system-init

hello-initd (scripted, /etc/init.d auto-detect + conf.d wrapper) — started manually:
  rc-service hello-initd start   # auto-detected at runtime, not referenced by boot
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

### Cron-Like Periodic Tasks
`cron-demo` runs a main process and, via `cron-command` + `cron-interval`,
fires a side task every 10s (after a 2s initial delay). `slinitctl catlog
cron-demo` shows both the main heartbeats and the cron ticks interleaved.

### Shared Multi-Service Logger
`shared-log` is a central logger reading lines from `stdin`. `app-one` and
`app-two` both declare `shared-logger = shared-log`, so slinit's
`SharedLogMux` multiplexes their stdouts into a single line-prefixed
stream (`[app-one] …`, `[app-two] …`). The central process appends each
line to `/run/shared-log.txt` with a timestamp.

### Runit Feature Bundle (`runit-svc`)
One service exercises most runit-inspired knobs:
- `env-dir = /etc/slinit.d/runit-svc.env.d` (one file per variable)
- `ready-check-command` with `ready-check-interval = 1`
- `pre-stop-hook` receives the PID as `$1`
- `control-command-HUP` — `slinitctl signal HUP runit-svc` triggers it
- `finish-command` receives exit code + signal as `$1` + `$2`
- `lock-file` guarantees single instance

Combined output lands in `/run/runit-svc.log`.

### Virtual TTY (`vtty-svc`)
Declares `vtty = true`; attach from another shell with
`slinitctl attach vtty-svc` (Ctrl+] to detach). The scrollback buffer is
set to 64 KiB via `vtty-scrollback`.

### /etc/init.d + OpenRC Compat
`/etc/init.d/hello-initd` is a plain SysV init script with LSB headers.
slinit auto-detects it, parses `Provides/Required-Start/...`, and wraps
every action in `sh -c '[ -r /etc/rc.conf ] && . /etc/rc.conf; [ -r
/etc/conf.d/hello-initd ] && . /etc/conf.d/hello-initd; exec <script>
<action>'`, so OpenRC tunables (`HELLO_MESSAGE`, `rc_parallel`) are
visible to the script without any schema changes.

Try:
```bash
rc-service hello-initd start       # argv shim over slinitctl
rc-update add hello-initd default  # adds waits-for: hello-initd on runlevel-default
rc-status default                  # graph runlevel-default
grep hello-initd /run/slinit/catch-all.log
                                   # see HELLO_MESSAGE from /etc/conf.d/
```

### Runit-Inspired Features

slinit integrates several features inspired by runit, adapted to dinit's
config-driven design:

- **finish-command**: post-exit script (receives exit code + signal as args)
- **ready-check-command**: polling-based readiness check (alternative to pipefd)
- **pre-stop-hook**: runs before SIGTERM (receives PID as arg)
- **control-command-SIGNAL**: custom signal handler scripts per signal
- **env-dir**: runit-style directory where each file = one env var
- **chroot / new-session / lock-file**: process isolation
- **close-stdin/stdout/stderr**: redirect fds to /dev/null
- **Log rotation**: logfile-max-size, logfile-max-files, logfile-rotate-time
- **Log filtering**: log-include/log-exclude regex patterns
- **Log processor**: script run on rotated files (e.g., gzip)
- **down file**: marker file in service dir prevents auto-start
- **pause/continue**: SIGSTOP/SIGCONT via slinitctl
- **once**: start service without auto-restart

## PID 1 Signal Handling

slinit handles SysV init signal conventions for compatibility with busybox and
other tools:

| Signal        | Action                | Source                        |
|---------------|-----------------------|-------------------------------|
| `SIGTERM`     | reboot                | busybox `reboot`              |
| `SIGINT`      | reboot                | Ctrl-Alt-Del (via CAD)        |
| `SIGQUIT`     | poweroff              | --                            |
| `SIGUSR1`     | reopen control socket | recovery when fs writable     |
| `SIGUSR2`     | poweroff              | busybox `poweroff`            |
| `SIGHUP`      | ignored               | --                            |
| `SIGRTMIN+3`  | halt                  | systemd-compat container      |
| `SIGRTMIN+4`  | poweroff              | systemd-compat container      |
| `SIGRTMIN+5`  | reboot                | systemd-compat container      |
| `SIGRTMIN+6`  | kexec                 | systemd-compat container      |

Signal-driven shutdowns can be gated by `/etc/slinit/shutdown.allow`
(or `/etc/shutdown.allow`). The gate applies only to the initial trigger;
a second press of Ctrl+Alt+Del (or repeated RT signal) always escalates.

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
