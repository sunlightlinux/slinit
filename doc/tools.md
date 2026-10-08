# slinit companion tools

Besides the daemon (`slinit`) and its control CLI (`slinitctl`), slinit
ships 43 tools: 42 Go binaries and `slinit-resource`, a shell OCF
resource agent. `go build ./...` builds all of them.

Each tool has a man page under [doc/man](man), which is the
authoritative reference. This document is an overview: what each tool
is for, followed by the invocations most worth knowing.

## Tool index

### Service execution and configuration

| Tool | Purpose | Manual |
|---|---|---|
| `slinit-runner` | Post-fork exec wrapper that applies sandboxing, seccomp, LSM transitions and capabilities. **Required** by any service using them | [slinit-runner(8)](man/slinit-runner.8.md) |
| [`slinit-check`](#slinit-check) | Offline (or `--online`) linter for service descriptions | [slinit-check(8)](man/slinit-check.8.md) |
| [`slinit-supports`](#slinit-supports) | Query whether a directive, opcode or feature is supported | [slinit-supports(8)](man/slinit-supports.8.md) |
| [`slinit-monitor`](#slinit-monitor) | Run a command on every service or environment change | [slinit-monitor(8)](man/slinit-monitor.8.md) |
| [`slinit-init-maker`](#slinit-init-maker) | Generate a minimal, bootable service directory | [slinit-init-maker(8)](man/slinit-init-maker.8.md) |

### Migration from other init systems

| Tool | Purpose | Manual |
|---|---|---|
| [`slinit-systemd-convert`](#slinit-systemd-convert) | Convert a systemd `.service` unit | [slinit-systemd-convert(8)](man/slinit-systemd-convert.8.md) |
| [`slinit-openrc-convert`](#slinit-openrc-convert) | Convert an OpenRC `init.d` script | [slinit-openrc-convert(8)](man/slinit-openrc-convert.8.md) |
| [`slinit-runit-convert`](#slinit-runit-convert) | Convert a runit service directory | [slinit-runit-convert(8)](man/slinit-runit-convert.8.md) |
| `slinit-sysvinit-convert` | Convert `/etc/inittab` | [slinit-sysvinit-convert(8)](man/slinit-sysvinit-convert.8.md) |

### Journal

| Tool | Purpose | Manual |
|---|---|---|
| [`slinit-journalctl`](#slinit-journalctl) | Query the journal; `journalctl`-compatible (65/65 flags) | [slinit-journalctl(8)](man/slinit-journalctl.8.md) |
| [`slinit-journald`](#slinit-journald) | Persistent journal daemon | [slinit-journald(8)](man/slinit-journald.8.md) |
| [`slinit-journal-migrate`](#slinit-journal-migrate) | Convert JSONL journal history to the binary format | [slinit-journal-migrate(8)](man/slinit-journal-migrate.8.md) |

### System daemons

| Tool | Purpose | Manual |
|---|---|---|
| `slinit-logind` | Login, seat and session manager (`org.freedesktop.login1`) | [slinit-logind(8)](man/slinit-logind.8.md) |
| [`slinit-logouthookd`](#slinit-logouthookd) | Write utmp logout records when sessions end | [slinit-logouthookd(8)](man/slinit-logouthookd.8.md) |
| `slinit-getty` | Minimal built-in login prompt | [slinit-getty(8)](man/slinit-getty.8.md) |
| `slinit-watchdogd` | Runtime hardware-watchdog petting daemon | [slinit-watchdogd(8)](man/slinit-watchdogd.8.md) |
| `slinit-mount` | autofs lazy-mount daemon | [slinit-mount(8)](man/slinit-mount.8.md) |
| `slinit-seedrng` | Persist entropy across reboots (SeedRNG) | [slinit-seedrng(8)](man/slinit-seedrng.8.md) |

### systemd-compatible utilities

| Tool | Purpose | Manual |
|---|---|---|
| [`slinit-sysusers`](#slinit-sysusers--slinit-tmpfiles) | Declarative user and group creation (`sysusers.d`) | [slinit-sysusers(8)](man/slinit-sysusers.8.md) |
| [`slinit-tmpfiles`](#slinit-sysusers--slinit-tmpfiles) | Declarative `/run` and `/var` bootstrap (`tmpfiles.d`) | [slinit-tmpfiles(8)](man/slinit-tmpfiles.8.md) |
| `slinit-sysctl` | Apply `sysctl.d` tunables | [slinit-sysctl(8)](man/slinit-sysctl.8.md) |
| `slinit-binfmt` | Register `binfmt_misc` formats | [slinit-binfmt(8)](man/slinit-binfmt.8.md) |
| `slinit-hostnamectl` | Query and set the hostname, without D-Bus | [slinit-hostnamectl(1)](man/slinit-hostnamectl.1.md) |
| `slinit-timedatectl` | Query and set time and date, without D-Bus | [slinit-timedatectl(1)](man/slinit-timedatectl.1.md) |
| [`slinit-cgtop`](#slinit-cgtop) | `top`-like viewer for the cgroup v2 tree | [slinit-cgtop(8)](man/slinit-cgtop.8.md) |

### OpenRC-compatible utilities

| Tool | Purpose | Manual |
|---|---|---|
| [`rc-service`](#openrc-compatibility-rc-service-rc-update-rc-status) | Service control shim over `slinitctl` | [rc-service(8)](man/rc-service.8.md) |
| [`rc-update`](#openrc-compatibility-rc-service-rc-update-rc-status) | Runlevel membership shim | [rc-update(8)](man/rc-update.8.md) |
| [`rc-status`](#openrc-compatibility-rc-service-rc-update-rc-status) | Status listing shim | [rc-status(8)](man/rc-status.8.md) |
| `slinit-start-stop-daemon` | Start or stop system daemons | [slinit-start-stop-daemon(8)](man/slinit-start-stop-daemon.8.md) |
| `slinit-supervise-daemon` | Run a non-forking daemon and restart it on crash | [slinit-supervise-daemon(8)](man/slinit-supervise-daemon.8.md) |
| `slinit-einfo` | `einfo` / `ewarn` / `ebegin` / `eend` status output | [slinit-einfo(8)](man/slinit-einfo.8.md) |
| `slinit-checkpath` | Create or verify paths with type, mode and ownership | [slinit-checkpath(8)](man/slinit-checkpath.8.md) |
| `slinit-fstabinfo` | Query `/etc/fstab` entries | [slinit-fstabinfo(8)](man/slinit-fstabinfo.8.md) |
| `slinit-mountinfo` | Query the kernel mount table | [slinit-mountinfo(8)](man/slinit-mountinfo.8.md) |
| `slinit-shell-var` | Sanitise strings into shell variable names | [slinit-shell-var(1)](man/slinit-shell-var.1.md) |
| `slinit-svc-value` | Per-service persistent key=value store | [slinit-svc-value(1)](man/slinit-svc-value.1.md) |

### sysvinit-compatible utilities

| Tool | Purpose | Manual |
|---|---|---|
| `slinit-killall5` | Signal every process except init, the caller and its session | [slinit-killall5(8)](man/slinit-killall5.8.md) |
| `slinit-fstab-decode` | Run a command with fstab-escaped arguments decoded | [slinit-fstab-decode(8)](man/slinit-fstab-decode.8.md) |

### Containers and high availability

| Tool | Purpose | Manual |
|---|---|---|
| `slinit-nspawn` | Launch a slinit container in new namespaces | [slinit-nspawn(8)](man/slinit-nspawn.8.md) |
| `slinit-machinectl` | Inspect and manage the local container registry | [slinit-machinectl(8)](man/slinit-machinectl.8.md) |
| `slinit-resource` | Pacemaker OCF resource agent for slinit services | [slinit-resource(7)](man/slinit-resource.7.md) |

### Shutdown and recovery

| Tool | Purpose | Manual |
|---|---|---|
| [`slinit-shutdown`](#slinit-shutdown) | Shut down, halt, reboot or soft-reboot | [slinit-shutdown(8)](man/slinit-shutdown.8.md) |
| [`slinit-nuke`](#slinit-nuke) | Emergency kill-all when orderly shutdown is unavailable | [slinit-nuke(8)](man/slinit-nuke.8.md) |

## Usage notes

### slinit-check

Configuration linter. It parses a service description exactly as the
daemon would, either offline or in the context of a running daemon:

```bash
# Offline mode (default)
slinit-check -d /etc/slinit.d myservice

# Online mode (queries running daemon for service dirs and env)
slinit-check --online myservice
slinit-check --online -p /run/slinit.ctl myservice
```

It checks file existence, type validity, command executability,
dependency references, dependency cycles and depth limits.

### slinit-monitor

Subscribes to the daemon's SERVICEEVENT / ENVEVENT stream and runs a
command for every change. Substitutions: `%n` (name), `%s` (status text
— `started`/`stopped`/`failed` for services, `set`/`unset` for env),
`%v` (env-var value, `-E` mode only), `%%` (literal percent).

```bash
# 1. Alert on failure of a specific critical service.
#    The shell test filters — slinit-monitor runs the command on every
#    transition; %s carries the resulting state.
slinit-monitor -c '[ "%s" = failed ] && \
    logger -p daemon.alert -t slinit "svc %n entered FAILED"' \
    postgres

# 2. Same idea via a webhook to the ops channel.
slinit-monitor -c '[ "%s" = failed ] && \
    curl -sS -X POST -d "%n went red" https://hooks.example/ops' \
    nginx redis postgres

# 3. Auto-remediation — restart a fallback svc when the primary drops.
slinit-monitor -c '[ "%s" = stopped ] && slinitctl start nginx-standby' \
    nginx

# 4. Fire once when a service reaches started, then exit.
#    Useful as a boot-time gate in shell scripts.
slinit-monitor -i -e -c 'true' database

# 5. Env-var watcher — mirror runtime env changes into an audit file.
slinit-monitor -E -c 'printf "%%s %n=%v\n" "$(date -Is)" >> \
    /var/log/slinit-env.log'
```

### slinit-journalctl

A `journalctl` implementation at 65/65 flag parity with systemd's. Live
queries go to slinit's control socket, whose in-memory ring buffer
covers the current boot; maintenance operations go to
`slinit-journald`'s admin socket. Packages install it as `journalctl`
too, so scripts written for systemd keep working.

```bash
# Live query — last 20 events, short-format
slinit-journalctl -n 20

# Follow (Ctrl-C to stop) with unit + priority filter
slinit-journalctl -u sshd -p err -f

# Regex on MESSAGE; case-insensitive since pattern is all-lowercase
slinit-journalctl -g "connection reset"

# Filter by identifier + boot; show one row per invocation
slinit-journalctl -t sshd --this-boot
slinit-journalctl -u nginx --list-invocations

# Introspection
slinit-journalctl --fields                    # list every known field
slinit-journalctl --header                    # ring-buffer / file metadata
slinit-journalctl --disk-usage                # bytes across on-disk journals
slinit-journalctl -F _HOSTNAME                # distinct values for a field

# Maintenance (via admin socket to slinit-journald)
slinit-journalctl --sync                      # fsync active sink
slinit-journalctl --rotate                    # close + rename active file
slinit-journalctl --vacuum-size=500M          # prune archived files
slinit-journalctl --flush                     # migrate volatile → persistent

# FSS sealing
slinit-journalctl --setup-keys                # mint sealing key (needs --force to overwrite)
slinit-journalctl --verify --file=/var/log/slinit-journal/2026-08-08.journal

# Message catalog (systemd-compatible)
slinit-journalctl --list-catalog              # every MESSAGE_ID with a catalog entry
slinit-journalctl -x -u myservice             # augment output with catalog text

# Journal namespaces
slinit-journalctl --list-namespaces
slinit-journalctl --namespace=prod -n 10

# Disk-image query (via losetup + mount, requires root)
slinit-journalctl --image=/path/to/disk.img --since=today
```

### slinit-journald

Persistent daemon that consumes events from slinit's event bus and
writes them to disk in one of two formats:

| Format | Properties |
|---|---|
| `binary` (default) | Compact, systemd-style layout; optional FSS sealing (TAG chain) |
| `jsonl` | One JSON object per line, gzip-compressed on rotation; easy to `grep` |

When the persistent directory is not writable, it falls back to a tmpfs
at `/run/slinit-journal/`; `slinit-journalctl --flush` moves the
volatile journal to persistent storage once `/var` is available.

```bash
# JSONL, default paths, gzip-rotated after 128 MiB
slinit-journald --format=jsonl

# Binary with FSS sealing, tag every 10 entries
slinit-journald --format=binary --fss-key=/etc/slinit/journal-key --fss-tag-every=10

# Named namespace — auto-suffixes dir/socket/pid/admin paths
slinit-journald --namespace=prod --format=jsonl

# Retention: prune archived files past caps
slinit-journald --vacuum-files=100 --vacuum-size=4294967296 --vacuum-age=720h  # size in bytes
```

### slinit-journal-migrate

One-time upgrade path from the JSONL format to the binary format. It
reads every `.jsonl` and `.jsonl.gz` file under `--from`, in filename
order, and appends the events to a single `YYYY-MM-DD.journal` file
under `--to`. Unparseable lines are skipped and counted. The source
directory is left untouched. The output is not FSS-sealed, and running
the tool twice on the same day appends the events again.

```bash
# Preview: list the source files that would be read
slinit-journal-migrate --from /var/log/slinit-journal \
    --to /var/log/slinit-journal-bin --dry-run

# Migrate
slinit-journal-migrate --from /var/log/slinit-journal \
    --to /var/log/slinit-journal-bin
```

### slinit-supports

Self-introspection CLI listing every directive slinit's parser
recognises, every wire opcode the control protocol carries, and
every named feature the daemon advertises. Companion to
`doc/features.md` for scripting-driven capability checks (a package
manager can query `slinit-supports <feature>` before enabling a
service that depends on it).

```bash
# Enumeration
slinit-supports --list-directives        # every `case "X":` in pkg/config/parser.go
slinit-supports --list-opcodes           # every Cmd*/Rply* in pkg/control/protocol.go
slinit-supports --list-all               # both, plus feature-name registry

# Direct lookup — exit 0 + descriptive text on hit; non-zero on miss
slinit-supports command
slinit-supports CmdStartService
slinit-supports journal-namespaces
```

### slinit-runit-convert

Port a runit `/etc/sv/<name>/` directory to a slinit service file.
Auto-detects `finish` (→ `finish-command`), `check` (→
`ready-check-command`), `down` (→ `manual = yes`), `conf` (→
`env-file`), and `log/run` (→ companion `<name>-log` service with
`consumer-of` + `log-type = pipe` on the primary). Recognises
`sv check DEP` inside run scripts and emits `waits-for: DEP`
automatically. `chpst` flags map to slinit equivalents where
possible (`-u` → `run-as`, `-C` → `working-dir`, `-o` →
`rlimit-nofile`, `-A N` → WARN since slinit has no runtime alarm).

```bash
# Single service to stdout
slinit-runit-convert /etc/sv/nginx

# Batch — every void sv dir into /etc/slinit.d, WARN/NOTE to stderr
slinit-runit-convert --output-dir=/etc/slinit.d --verbose /etc/sv/*

# Preview + enable-map: also print `slinitctl enable` for services
# that were enabled under runit (/var/service symlinks)
slinit-runit-convert --dry-run --enable-map /etc/sv/*
```

### slinit-openrc-convert

Port an OpenRC `/etc/init.d/*` script to a slinit service file.
Two paths: (1) variable-only scripts (`command=`, `pidfile=`,
`depend()`, no custom `start()`/`stop()`) become self-contained
slinit files with no runtime openrc-run dependency; (2) scripts
with custom shell functions (the common case) get wrapped as
`command = /usr/sbin/openrc-run <script> start`, preserving every
`ebegin`/`einfo`/`start-stop-daemon` call. `depend()` verbs map:
`need` → `depends-on:`, `use`/`after` → `waits-for:`; `before`
warns (invert on the target); `provide`/`keyword` note the
semantic gap. Auto-detects `/etc/conf.d/<name>` as env-file.

```bash
# Single script to stdout
slinit-openrc-convert /etc/init.d/dbus

# Batch conversion + `slinitctl enable` hints for whatever was in
# /etc/runlevels/*/  (--enable-map)
slinit-openrc-convert --output-dir=/etc/slinit.d --enable-map /etc/init.d/*

# Override the wrapper (default: /usr/sbin/openrc-run)
slinit-openrc-convert --wrapper=/usr/bin/slinit-openrc-shim /etc/init.d/*
```

### slinit-systemd-convert

Port a systemd `.service` unit to a slinit service file. Section-
aware INI parser handles `\`-line continuation, and ~40 [Unit] +
[Service] + [Install] directives are mapped. `Type=` maps as
`simple`/`exec` → `process`, `forking` → `bgprocess`, `oneshot` →
`scripted`; `Restart=` collapses systemd's 5 values into slinit's 3
(no/yes/on-failure) with ambiguous cases warned. `User`+`Group`
merge into `run-as = user:group`. Hardening directives (`Private*`,
`Protect*`, `Restrict*`, `SystemCall*`) produce NOTEs naming the
equivalent slinit directive. Dep names normalise — strips
`.service`, `.target`, `.socket`, `.path`, `.mount`, `.timer`,
`.swap`, `.device` so slinit sees bare names. Timer / socket /
path / mount / target units are rejected at the guard — those
need slinit-native equivalents, not mechanical translation.

```bash
# Single unit to stdout
slinit-systemd-convert /lib/systemd/system/sshd.service

# Batch — every service unit into /etc/slinit.d
slinit-systemd-convert --output-dir=/etc/slinit.d /lib/systemd/system/*.service
```

### slinit-cgtop

Top-like viewer for the cgroup v2 tree. Reads `/sys/fs/cgroup`
periodically and surfaces per-cgroup CPU %, memory bytes, and task
counts. Colour-free output is script-friendly with `--once`.

```bash
# Live view, refresh every second, top 3 levels of the cgroup tree
slinit-cgtop

# Sort by memory instead of CPU, drill deeper, refresh every 500ms
slinit-cgtop --sort mem --depth 5 --delay 500ms

# One snapshot for a cron / metrics scraper (single line per cgroup)
slinit-cgtop --once --sort mem --depth 4

# Show every cgroup — including idle ones with zero tasks / zero memory
slinit-cgtop --all
```

### slinit-sysusers / slinit-tmpfiles

Declarative bootstrap of system state, drop-in compatible with the
`systemd-sysusers`(8) and `systemd-tmpfiles`(8) formats. Typically wired
as `type = scripted` services early in the boot graph.

```bash
# Users & groups — reads /usr/lib/sysusers.d/*.conf + /etc/sysusers.d/*.conf
slinit-sysusers                  # apply everything
slinit-sysusers --dry-run        # preview actions without touching passwd/group
slinit-sysusers --dirs /etc/sysusers.d  # override search path

# Runtime paths — reads /usr/lib/tmpfiles.d/*.conf + /etc/tmpfiles.d/*.conf
slinit-tmpfiles                  # create/clean per config
slinit-tmpfiles --dry-run
```

### slinit-logouthookd

Persistent daemon that writes UTMPX `DEAD_PROCESS` records when tty /
pty sessions end. Login programs (agetty, sshd, su) can drop a session
descriptor onto its Unix socket and forget about it — the daemon
watches the session, and once the last process on that line is gone,
it writes the logout record so `who`, `w`, and `last` stay accurate
without every login shell needing a private hook.

```bash
# Standard invocation (as a slinit service — usually a `type = process`).
slinit-logouthookd

# Custom socket path + permissions (default: /run/slinit-logouthookd.sock, 0600)
slinit-logouthookd --socket /run/slh.sock --perms 0660
```

### slinit-init-maker

Generates a bootable service-description directory skeleton — top-level
`boot` target, a `system-init` marker, `system-mounts` (on by default)
and an optional `network` stub, N agetty services with their
inittab-id, an `env` file with `PATH` (plus `HOSTNAME`/`TZ` when given)
to pass to `slinit --env-file`, an optional shutdown-hook sample, and a
README. Inspired by
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

### OpenRC compatibility: rc-service, rc-update, rc-status

Thin argument-translating shims over `slinitctl` for administrators
used to OpenRC. Each one executes `slinitctl` (found via `$PATH`, or
the `SLINITCTL` environment variable), so output, exit codes and flag
handling are exactly `slinitctl`'s.

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
rc-update show                # → slinitctl graph (full graph, DOT)
rc-update update              # no-op (slinit has no dep cache)

# rc-status — status listing
rc-status                     # → slinitctl list
rc-status default             # → slinitctl graph (full graph, DOT)
rc-status --list              # list known OpenRC runlevel names
rc-status --runlevel          # print "default" (slinit has no current runlevel)
```

`/etc/rc.conf` and `/etc/conf.d/<name>` are sourced automatically
before every `init.d` script action, so OpenRC per-service
configuration such as `/etc/conf.d/nginx` keeps working unchanged.

