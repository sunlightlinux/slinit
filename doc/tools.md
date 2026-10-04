# slinit companion tools

slinit ships 43 tools besides the daemon and `slinitctl` (42 Go binaries
plus `slinit-resource`, a shell OCF resource agent): linters,
converters from other init systems, drop-in clones of OpenRC and systemd
utilities, the journal pipeline, and the container helpers. Every one of
them has a man page under [doc/man](man) — those are the reference. This
file is the tour: what each tool is for, and the invocations worth
knowing.

`go build ./...` builds all of them.

This file was split out of the README when the README was trimmed for
the 3.0 line.


### slinit-check

Configuration linter. Validates service files offline or using a running daemon's context:

```bash
# Offline mode (default)
slinit-check -d /etc/slinit.d myservice

# Online mode (queries running daemon for service dirs and env)
slinit-check --online myservice
slinit-check --online -p /run/slinit.ctl myservice
```

Checks: file existence, type validity, command executability, dependency references, circular dependencies, depth limits.

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

### slinit-journalctl / journalctl

Systemd `journalctl` at 65/65 flag parity. Talks to slinit's control
socket for live queries (the in-process ring buffer covers the
current boot) and to `slinit-journald`'s admin socket for
maintenance ops. Ships as a `journalctl` symlink so scripts written
for systemd's binary keep working.

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

Persistent daemon consuming events from slinit's event bus and
writing them to disk. Supports both Phase C (JSONL, gzip-rotated,
human-grep-friendly) and Phase B (binary, systemd-compatible with
optional FSS TAG chain). Falls back to tmpfs at
`/run/slinit-journal/` when the persistent primary is unwritable.
`--flush` migrates volatile to persistent once /var comes online.

```bash
# JSONL, default paths, gzip-rotated after 128 MiB
slinit-journald --format=jsonl

# Binary with FSS sealing, tag every 10 entries
slinit-journald --format=binary --fss-key=/etc/slinit/journal-key --fss-tag-every=10

# Named namespace — auto-suffixes dir/socket/pid/admin paths
slinit-journald --namespace=prod --format=jsonl

# Retention: prune archived files past caps
slinit-journald --vacuum-files=100 --vacuum-size=4G --vacuum-age=720h
```

### slinit-journal-migrate

Cross-format journal migration helper. Converts a Phase C JSONL
directory to Phase B binary (or vice versa) so operators can
switch storage formats without losing history.

```bash
# JSONL → binary, seals with an existing FSS key
slinit-journal-migrate --from=jsonl --to=binary \
    --input-dir=/var/log/slinit-journal \
    --output-dir=/var/log/slinit-journal.binary \
    --fss-key=/etc/slinit/journal-key

# Binary → JSONL (useful for grep pipelines on old archives)
slinit-journal-migrate --from=binary --to=jsonl \
    --input-dir=/var/log/slinit-journal \
    --output-dir=/tmp/slinit-journal.jsonl
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
`boot` target, optional `system-mounts` + `network` stubs, N agetty
services (with correct inittab-id), env-file with `HOSTNAME`/`TZ`/`PATH`,
optional shutdown-hook sample, README. Inspired by
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

### OpenRC compat: rc-service / rc-update / rc-status

Thin argv-translating shims over `slinitctl` for admins used to
OpenRC. They exec `slinitctl` (resolved via `$PATH` or the `SLINITCTL`
env var), so output, exit codes and flag precedence mirror
`slinitctl`'s own.

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
rc-update show                # → slinitctl graph runlevel-default
rc-update update              # no-op (slinit has no dep cache)

# rc-status — status listing
rc-status                     # → slinitctl list
rc-status default             # → slinitctl graph runlevel-default
rc-status --list              # list known OpenRC runlevel names
rc-status --runlevel          # print "default" (slinit has no current runlevel)
```

`/etc/rc.conf` and `/etc/conf.d/<name>` are sourced automatically
before every init.d script action (see Project structure notes),
so OpenRC per-service config files like `/etc/conf.d/nginx` keep
working unchanged.

