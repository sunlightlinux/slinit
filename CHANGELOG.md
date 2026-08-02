# Changelog

All notable changes to slinit are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Development from **v2.0.0** onward focuses on three tracks:

- **New features** — targeted, additive; no gratuitous surface growth.
- **Security features** — hardening knobs, fail-closed contracts,
  supply-chain hygiene.
- **Code fixing** — regressions, upstream parity, quality, refactors
  that stay behind existing behaviour.

Pre-v2.0.0 history is summarised at the bottom; `git log v1.10.55` has
the full commit-level record.

## [Unreleased]

### Added

- **Interactive rescue menu on fatal boot failure** (`pkg/recovery`).
  When PID 1 fails to load any boot service (typo in a service file,
  missing dependency, unreadable /etc/slinit.d), the previous
  behaviour was `sleep 10 && reboot` — a reboot-loop trap that hid
  the diagnosis and gave no in-console path to fix the config
  without an install USB. Now: print a boxed menu on /dev/console
  with the collected load errors, wait up to 60s for operator
  input, then execute one of:
  - `r` — reboot now
  - `p` — power off
  - `s` (or Ctrl-B) — drop to shell (sulogin first, then /bin/sh);
    on shell exit, the menu re-appears so the operator can fix a
    typo and press `c` to retry without a real reboot
  - `c` (or Ctrl-D) — retry loading boot services from scratch
  - no input → auto-reboot after timeout (headless-safety net)

  The Ctrl-B / Ctrl-D shortcuts align with muscle-memory from
  common boot debuggers. Truncates over-long error lines so the
  menu box stays visually intact on 80-col serial consoles.
  Bypasses cleanly when `/dev/console` isn't openable (truly
  headless with no console → straight to auto-reboot).

- **Post-boot-collapse rescue menu unified with `pkg/recovery`**
  (`recovery.PresentCollapse`). The old `confirmRestartBoot` prompt
  fired from `cmd/slinit/main.go` when all services stopped without
  an explicit shutdown was a plain "Choose: (r)eboot, r(e)covery,
  re(s)tart boot sequence, (p)ower off?" line with no timeout, no
  Ctrl-B/Ctrl-D shortcuts, and no stale-input flush. Replaced with a
  boxed menu that reuses the same cbreak + tcflush + single-keypress
  + 60s auto-reboot machinery as the load-fail rescue menu:
  - `r` — reboot now
  - `p` — power off
  - `s` (or Ctrl-D) — restart boot sequence (retry all boot
    services; Ctrl-D matches "continue booting" muscle memory)
  - `e` (or Ctrl-B) — start `recovery` service (Ctrl-B matches
    "escape hatch" muscle memory)
  - no input → auto-reboot after 60s (headless-safety net; previously
    would block forever on `f.Read` waiting for a key)

  Same visual language as the load-fail menu so the two boot-failure
  prompts feel like siblings, and the tcflush guarantees stray input
  buffered at collapse time (kernel messages on serial, operator's
  Enter-presses, QEMU chatter) can't auto-dismiss the menu before
  the operator sees it.

## [2.1.0] — 2026-08-01

Second release under the v2.x line. Themes of this cut:

- **Full journal pipeline.** slinit ships an operator-grade journal
  subsystem alongside the existing per-service logfile/catlog
  surface. Coexisting formats via `slinit-journald --format=`:
  Phase C JSONL text (debuggable, greppable, `zcat | jq`-friendly)
  and Phase B binary (structurally isomorphic to systemd-journald
  with 7 object types, jenkins lookup3 hashing, entry-array time
  index, FSS sealing via HKDF-SHA256 + HMAC-SHA256). New
  `slinit-journalctl` CLI covers the operator daily workflow
  (short/short-iso/cat/json/verbose/export formatters, `-u`, `-p`,
  `--since/--until`, `-r`, `-f`, `-k`, `--list-boots`, `-b/--boot`,
  `-c/--cursor`, `--show-cursor`, `--file`, `--verify`), reads both
  formats via magic-sniff, follows via CmdJournalSubscribe.
  Rotation (128 MiB / 24h), vacuum (100 files / 4 GiB / 30 days),
  gzip-on-rotate for JSONL, volatile /run fallback when /var is
  unwritable. Backlog replay at daemon start so events emitted
  before `journal-demo` binds still land on disk. Migrator
  (`slinit-journal-migrate`) converts JSONL history into the
  binary format. `sd_journal`-semantic Go API (`pkg/journalbin/sd`
  — 15 methods, no cgo, no libsystemd link). Demo VM ships
  everything wired with FSS key minted at initramfs-build time.

- **100% dinit-parity closure.** Deep audit against dinit
  `2b25539` confirmed protocol opcodes 0..30, all 22 dinitctl
  subcommands, every `dinit-service.5` directive, `@meta` +
  `@include*` all match. Five silent-surprise gaps in the env-var
  and bootstrap-path layer were closed: `DINIT_SERVICE`,
  `DINIT_CS_FD`, `DINIT_SOCKET_PATH` alias, auto-load
  `/etc/slinit/environment` (with `/etc/dinit/environment`
  fallback), user-mode `$XDG_CONFIG_HOME` + `$HOME/.config` dedup.
  Two new V7 opcodes for race-free wait-for-stop:
  `CmdRmDepV7 = 30` (dinit-compat, mirrors dinit
  `REM_DEP_V7`) and `CmdDisableServiceV7 = 62` (slinit-native
  atomic disable). `slinitctl disable --dinit-compat` speaks the
  A-wire path (CmdRmDepV7 + client-side symlink cleanup via new
  `CmdQueryServiceLoadDir = 63`) for interop with real dinit
  daemons.

- **Self-introspection: `slinit-supports` CLI + `doc/features.md`.**
  Distinctive — neither systemd nor dinit ship an equivalent.
  Answers "does slinit support X?" for X = directive / opcode /
  option, and where the feature originated (dinit / systemd /
  runit / s6 / OpenRC / Upstart / slinit-native). Hybrid design:
  the canonical list is auto-discovered from `pkg/config/parser.go`
  and `pkg/control/protocol.go` via `go/ast`, so drift between
  "docs claim" and "code accepts" is structurally impossible.
  Provenance annotations hand-curated in `pkg/features/provenance.go`.
  CI test fails on orphans (annotated names removed from code),
  warn-only on unannotated (accumulation acceptable — enrichment
  is incremental). `slinit-supports NAME` / `--list-{directives,
  opcodes,options,all}` / `--group-by=source|category|kind` /
  `--format=text|json|markdown`. `doc/features.md` is the
  regenerable canonical feature reference committed under source.

- **Journal UX polish.** `SLINIT_TARGET_PID` field so short-format
  renderers display the SUBJECT service's PID in `unit[PID]:`
  brackets instead of slinit's own PID=1 (the emitter). No
  bracket at all for internal services / already-exited scripted
  services rather than the misleading `[1]`. Kernel events
  correctly show `kernel:` (not `unknown[1]:`) with no user-space
  identity leak. `journalctl` symlink to `slinit-journalctl` in
  the demo for muscle-memory. `-b` systemd shortcut for `--boot`
  (accepts `0`, hex ID, deferred `-N` relative). Priority keyword
  auto-recognition — `INFO:` / `ERROR:` / `WARN:` prefixes map to
  syslog severities without requiring RFC 5424 `<N>` framing.

- **Shutdown console: getty prompt no longer collides with the
  first [STOPPD] line.** Cursor-reset + clear-line + newline
  sequence on the transition into shutdown mode.

Zero behavioural breakage from v2.0.0; the bump reflects the
substantial new feature surface (journal pipeline + self-introspection)
rather than any incompatibility. Everything under [2.0.0]'s known
limitations still holds.

### Added

- **`SLINIT_TARGET_PID` for short-format `unit[PID]:` display.**
  State-transition events are emitted by slinit itself (PID 1) but
  the operator wants `system-init[478]: STARTED` — the bracket
  should show the SUBJECT service's PID, not the emitter's.
  `emitJournalStateEvent` now stashes the target service's PID via
  ServiceSet lookup; `emitJournalLogLine` stashes it via a new
  `GetPID` callback on `LogRotatorConfig` (wired from ProcessService).
  slinit-journalctl short/short-iso renderers prefer the target PID
  when present, falling back to `_PID` (the emitter) otherwise.
  Internal services (system-init, boot, all-services) and pre-start
  events with PID ≤ 0 skip the field so no misleading `[0]` /
  `[-1]` brackets. Kernel events (Transport=kernel) untouched.

- **Slinit-native disable atomic + dinit-compat wire:
  `CmdDisableServiceV7 = 62` + `CmdQueryServiceLoadDir = 63` +
  `slinitctl disable --dinit-compat` flag.** Two wires on the
  server for slinit's disable — the slinit-native atomic path
  (`CmdDisableServiceV7`, single round-trip: rm-dep +
  waits-for.d symlink cleanup + StopService + inline status) and
  the dinit-compat path (`CmdRmDepV7` from the prior commit + a
  new `CmdQueryServiceLoadDir` opcode so clients can locate the
  per-service load directory to remove waits-for.d/target
  symlinks client-side). `slinitctl disable` defaults to the
  atomic slinit path (V7 when peer ≥ 7, plain otherwise); the
  new `--dinit-compat` flag switches to the client-side symlink
  cleanup flow, wire-compatible with real dinit daemons that
  don't know slinit's atomic opcode. Falls back to `boot` as the
  "from" service when `--from` isn't given (matches slinit's
  server-side default). Remote-friendly: the atomic path needs no
  filesystem access at the client; the dinit-compat path warns
  and continues when the symlink can't be reached (runtime
  removal already succeeded).

- **`slinit-supports` — self-introspection CLI + `doc/features.md`.**
  Answers "does slinit accept X?" for X = directive, opcode, or
  option — and where the feature originated (dinit / systemd / runit
  / s6 / OpenRC / Upstart / slinit-native). Hybrid design: the
  canonical list is auto-discovered from source via `go/ast` (walks
  `applySetting`'s switch dispatcher in `pkg/config/parser.go` and
  the `Cmd*` const block in `pkg/control/protocol.go`), so drift
  between "docs claim we support this" and "code actually accepts
  this" is structurally impossible. Provenance annotations
  (source/category/notes) hand-curated in `pkg/features/provenance.go`;
  a CI test fails on orphans (annotated names removed from code
  without cleanup). Unannotated discovered names get TODO
  placeholders — enrichment is incremental. Commands:
  `slinit-supports NAME` (yes/no + provenance), `--list-directives`
  / `--list-opcodes` / `--list-options` / `--list-all` (enumerate,
  optionally `--group-by=source|category|kind`), `--format=text|
  json|markdown`. `doc/features.md` is the markdown output committed
  under source control — regenerate with
  `slinit-supports --format=markdown --list-all --group-by=source
  > doc/features.md`. Distinctive: neither systemd nor dinit ships
  an equivalent self-introspection tool.

- **Full dinit-parity sweep: env-var + bootstrap-path gaps closed.**
  A deep audit of dinit's protocol, dinitctl subcommands, service
  directives, and bootstrap surface (against dinit 2b25539) surfaced
  five silent-surprise gaps for operators porting a dinit setup —
  all in the environment/env-file bootstrap layer, not opcodes or
  config grammar (which stayed at 100% parity). Fixed here as a
  single sweep so the audit's punch-list closes cleanly:
  - `DINIT_SERVICE` env var now exported alongside `DINIT_SERVICENAME`
    under `load-options: export-service-name`. Ported scripts using
    `case "$DINIT_SERVICE" in …` work unchanged.
  - `DINIT_CS_FD` exported alongside `SLINIT_CS_FD` under
    `options: pass-cs-fd`, so a dinit-native child inheriting the
    control-socket fd finds it under the documented name.
  - `slinitctl` honours `DINIT_SOCKET_PATH` (and `SLINIT_SOCKET_PATH`
    as native alias) as a pre-mode fallback when `--socket-path` is
    absent. DINIT_ wins on collision to match dinit's behaviour
    exactly.
  - slinit auto-loads `/etc/slinit/environment` (and
    `/etc/dinit/environment` as second-choice fallback) when
    `--env-file` isn't given. Missing-file is silently skipped;
    explicit `--env-file` keeps the existing loud-error semantics.
  - User-mode service-dir search list now includes BOTH
    `$XDG_CONFIG_HOME/slinit.d` AND `$HOME/.config/slinit.d` when
    they differ, deduped when identical. Users with
    non-default XDG_CONFIG_HOME no longer lose their `~/.config`
    overrides.

- **Dinit upstream sync: `CmdRmDepV7 = 30`** (matches dinit
  `2b25539`). Server-side handler mirrors the ENABLE_SERVICE_V7
  wire — reply is `[RplyServiceStatus][dep_exists(1B)][status_v6(22B)]`
  instead of a bare RplyACK — so a client learns the target's
  post-removal state on the same round-trip. `slinitctl rm-dep`
  uses V7 automatically when the peer advertises CPVersion ≥ 7,
  falling back to the plain CmdRmDep + ACK path on older daemons
  so mixed-version pairs keep working. Closes the tiny race where a
  follow-up status query could catch the target mid-transition.
  Rendered in slinitctl output as `(target now STOPPED)` /
  `STARTING` / etc.

- **`slinit-journalctl -o export`** — systemd export format
  (`KEY=value` lines, blank line between events). Piped to
  systemd-journal-remote-alikes or custom parsers for cross-host
  log forwarding without JSON overhead. Empty fields skipped for
  readability (matches renderVerbose convention). Binary payloads
  not supported in v1 — slinit's Event schema never emits binary
  values anywhere, so the length-prefixed escape systemd uses is
  deferred until actually needed.

- **Vacuum for binary journals.** `pkg/journald.VacuumOptions` gains
  a `Suffixes` field (default keeps `.jsonl` back-compat); binary
  callers pass `[".journal"]` and prune the same way JSONL already
  did. `slinit-journald --format=binary` wires
  `VacuumingHook(..., Suffixes=[".journal"])` through its RotatedHook
  so binary-mode operators get identical retention behaviour to the
  JSONL sink. `removeJournalFile` now also cleans up `.gz` and
  `.idx` companions in one go.

- **Journal Phase B — binary format + FSS sealing.** Adds an
  on-disk binary journal (`pkg/journalbin`) structurally isomorphic
  to systemd-journald's format (7 object types: DATA, FIELD,
  ENTRY, DATA_HASH_TABLE, FIELD_HASH_TABLE, ENTRY_ARRAY, TAG; 240-B
  header; jenkins lookup3 hashing; entry-array chain for time-bisect;
  little-endian throughout) but with a distinct magic (`SLJRNL01`)
  so `journalctl` from systemd cannot open slinit files by mistake.
  DATA dedup via hash table saves storage on high-cardinality workloads.
  Forward Secure Sealing (FSS) via HKDF-SHA256 per-epoch key + HMAC-SHA256
  tag chain; `slinit-journalctl --file X --verify --fss-key /path`
  walks the chain and reports first tamper point. Coexists with the
  Phase C JSONL sink — `slinit-journald --format=binary|jsonl`,
  default binary; JSONL stays available for debug workflows that want
  greppable text logs. `slinit-journal-migrate --from DIR --to DIR`
  converts existing JSONL history into the binary format. New
  sd_journal-semantic Go API at `pkg/journalbin/sd` (Open, Next,
  Previous, GetData, GetRealtimeUsec, GetCursor, TestCursor,
  SeekRealtimeUsec, SeekCursor, SeekHead, SeekTail, AddMatch,
  FlushMatches) — semantic-compat with libsystemd-journal, not ABI-
  compat (no cgo, no C linking). Demo QEMU image wires
  journal-demo with binary format + FSS out-of-the-box; key minted
  at initramfs-build time so sealing works on first boot.


- **Journal pipeline Phase 2 — `slinit-journalctl` query CLI.**
  systemd-journalctl-equivalent operator surface on top of the Phase
  1 event bus. Wire path: `slinit-journalctl → CmdJournalQuery /
  CmdJournalSubscribe → journal.GlobalBuffer().Query / GlobalSubscribe
  → RplyJournalEntry stream`. Flags for v2: `-n/--lines`, `-o/--output`
  (short / short-iso / cat / json / verbose), `-u/--unit` (repeatable),
  `-p/--priority` (numeric + symbolic), `--since/--until` (RFC3339,
  `now/today/yesterday`, relative `-Nh/-Nd`), `-r/--reverse`,
  `-f/--follow`, `-k/--dmesg`, `--list-boots`, `--boot [ID]`,
  `-c/--cursor`, `--show-cursor`, `--file=PATH` (JSONL offline reader
  incl. transparent `.gz`), `--socket-path`, `--system/--user`.
  Kmsg reader wired into slinit itself so `-k` populates on
  system-mode boots.
- **Journal pipeline Phase 3 — `slinit-journald` persistent daemon.**
  Binds `/run/slinit/events.sock` with SO_PASSCRED, snapshots
  `/proc/PID/{comm,exe,cmdline}` for trusted metadata on external
  clients. Persists JSONL to `/var/log/slinit-journal/YYYY-MM-DD.jsonl`
  with `.idx` bisect companion (16-byte tuples `(realtime_usec_le64,
  byte_offset_le64)`). Rotation defaults 128 MiB / 24h; vacuum defaults
  100 files / 4 GiB / 30 days. Whole-file gzip compression on rotate
  (chosen over LZ4 to avoid a new external dependency; readers open
  `.jsonl.gz` transparently). Volatile fallback to `/run/slinit-journal/`
  when `/var/log/slinit-journal/` is unwritable (missing partition,
  container without persistent mount). Daemon is optional — slinit
  keeps working with or without it.

### Changed

- **`extractSyslogLevel` now recognizes uppercase keyword prefixes**
  (`EMERG:`, `ALERT:`, `CRIT:`, `ERR:`/`ERROR:`, `WARN:`/`WARNING:`,
  `NOTICE:`, `INFO:`, `DEBUG:`) in addition to the RFC 5424 `<N>`
  form. Priority-based filters (`log-level-max`, `alert-level`) and
  the new journal pipeline (`slinit-journalctl -p err`) now behave
  the way an operator expects when apps use stdlib-style level tags
  instead of syslog priorities. RFC 5424 keeps precedence; a line
  with neither still defaults to info. Case-sensitive uppercase +
  colon terminator only, so plain prose containing "info" or the
  word "error" is unaffected.

### Fixed

- **`slinit-journald`: pre-daemon events reach the persistent
  journal.** Before this change, service state transitions and log
  captures emitted by slinit BEFORE `journal-demo` bound
  `/run/slinit/events.sock` were only in the in-proc ring buffer
  (queryable via `slinit-journalctl` sans `--file`) — the binary/
  JSONL file on disk only picked up events from daemon-start onwards.
  slinit-journald now queries slinit's control socket at startup
  (`--control-socket=/run/slinit.socket`, empty disables) via
  `CmdJournalQuery{}` and persists every returned backlog event
  through the configured sink. Small race window (~<10ms between
  query completion and `events.sock` bind) is documented and
  accepted; a seq-based dedup would need protocol additions we can
  add later if operators hit the race in practice.

- **Shutdown console: getty prompt no longer collides with the first
  [STOPPD] line.** On systems where a getty is still active on the
  same tty slinit uses as console (typically tty1 on bare metal),
  the login prompt "sunlight login: " previously ran into slinit's
  first shutdown status line ("sunlight login: [STOPPD] boot"). The
  boot-console renderer now emits a cursor-reset + clear-line +
  newline sequence on the transition into shutdown mode, so the
  first [STOPPD] line always starts on a fresh row. No behavioural
  change to the [STOPPD] cascade itself.

## [2.0.0] — 2026-07-26

First release under the v2.x line. Marks the production-maturity
milestone for slinit: the feature surface converged, docs align 1:1
with code, and both test suites are green end-to-end on live hardware.

No behavioural breakage from v1.10.55 — the bump signals the
stable-feature-branch commitment rather than an API change. Everything
that ships in v1.10.55 works identically in v2.0.0; the difference is
the release policy (see the header of this file for the three v2.x
lanes: new features, security features, code fixing).

## [1.10.55] — 2026-07-26

Terminal release of the v1.x line. Every directive documented in
[`slinit-service(5)`](doc/man/slinit-service.5.md), every binary has a
man page, and every operator-visible subcommand has a live-VM
acceptance case.

### Coverage
- Unit: ~1640 tests across ~40 packages (227 `_test.go` files)
- Functional (QEMU): **201** tests
- Acceptance (SSH-driven, live VM): **197** cases
- Fuzz: 21 targets
- Directives: 299 (all documented)
- Control protocol: v7 (min-compat v1)

### Highlights since v1.10.41

**Test coverage — closed all real gaps.**
- Functional batch 167-201 (35 tests): pre/post-start hooks,
  reload-signal, TTY cluster, restrict-*, D-Bus optional integration,
  PSI cpu/io pressure, kill/timeout clusters, cgroup expanded set,
  service-directory modes+quotas, standard-input text/data,
  exec-condition, import-credential, LSM fail-closed, Bucket B legacy,
  seccomp arch+log+MDWE, options flag clusters.
- Acceptance batch 170-197 (28 tests): freeze/thaw, reset-failed,
  transient `slinitctl run`, profile triad, add/rm-dep runtime,
  wake/release, untrigger, query cluster, is-newer/older-than,
  scheduled shutdown, reload round-trip, signal end-to-end, action
  end-to-end, global env persistence, starts-on-console arbiter,
  shares-console interplay, service-template lifecycle, dbus-name
  auto-wire, apparmor-real load, slinit-check --online, slinit-monitor
  end-to-end, pass-cs-fd, per-svc env round-trip, metadata render,
  cron-persistent+jitter.

**Systemd parity — Buckets A/B/C/D/E across ~250 directives.**
- Hardening: `restrict-*` arg-checking BPF (realtime / namespaces /
  suidsgid / file-systems / address-families), `memory-deny-write-execute`,
  full `protect-*` cluster (kernel-tunables / -modules / -logs /
  -clock / -control-groups / -hostname), `lock-personality`.
- PSI pressure watches: `{memory,cpu,io}-pressure-{watch,threshold}`
  with SvcEvent codes.
- Credentials pipeline: `load-credential`, `set-credential`,
  `import-credential` on tmpfs-ro at `$CREDENTIALS_DIRECTORY`.
- TTY cluster: `tty-path` + `tty-columns/rows/vhangup/vt-disallocate/reset`.
- D-Bus **optional** integration: `bus-name` / `bus-policy` /
  `bus-name-scope` auto-wire a ready-check via `dbus-send` when the
  binary is present; slinit itself ships zero D-Bus dependency.
- Start predicates (~35): `condition-*` / `assert-*`,
  `exec-condition`, `condition-fraction`, `condition-path-is-socket`,
  `condition-security=measured-os`.
- Restart cluster: `restart-randomized-delay`, `restart-max-delay`,
  `restart-force-exit-status`, `restart-mode`, `restart-kill-signal`,
  `start-limit-action`.
- Timeout cluster: `timeout-sec`, `timeout-abort-sec`,
  `timeout-{start,stop}-failure-mode`, `job-timeout-sec`.
- Kill semantics: `kill-mode`, `final-kill-signal`,
  `survive-final-kill-signal`, `watchdog-signal`.
- cgroup v2: full memory / cpu / io / pids / cpuset knob set plus
  `cpuset-partition`, `startup-allowed-cpus`, `startup-allowed-memory-nodes`,
  `cgroup-setting` (generic).
- `runtime-max-sec` + `runtime-randomized-extra`, `exit-type=main|cgroup`,
  `oom-policy`, `pre-start-command` / `post-start-command`.
- Env pipeline: `pass-environment`, `unset-environment`,
  `exec-search-path`, `env-generator`, `setenv`.
- `standard-input-text` / `standard-input-data`, `open-file`,
  `notify-access`, `guess-main-pid`, `dynamic-user`,
  `file-descriptor-store-max` + `-preserve`.
- Service directories: full auto-managed cluster
  (`runtime/state/cache/logs/configuration-directory` + `-mode` +
  `-quota` + `-accounting`).
- v261 catch-up: 7 items shipped (PSI, condition-fraction,
  condition-path-is-socket, condition-security=measured-os,
  `--minimum-uptime-sec`, `memory-thp`, `file-descriptor-store-preserve`).

**Runit / s6-linux-init / OpenRC / upstart parity — backlogs closed.**
- Runit ergonomics: `finish-command`, `ready-check-command`,
  `pre-stop-hook`, `env-dir`, `control-command-<SIGNAL>`, `chroot`,
  `new-session`, `lock-file`, `close-fds`, log rotation/filtering/
  processor, down-file marker, `once` command,
  `supplementary-groups`.
- s6-linux-init: catch-all logger, TAI64N / ISO-8601 / wallclock / none
  timestamps, scheduled shutdown + cancel + status, wall broadcasts,
  `/etc/shutdown.allow` access control, `--wait-fd` container-manager
  entrypoint sync, kernel-cmdline snapshot, `/run` tmpfs staging modes,
  `--rlimits` global limits, RT-signal container shutdown
  (SIGRTMIN+3..+6), UTMPX logout + wtmp RUN_LVL, `slinit-init-maker`,
  `slinit-nuke`, `slinit-logouthookd`, `bundle-of`, `log-select`,
  `--persist-intent`.
- OpenRC UX: `rc-service` / `rc-update` / `rc-status` argv shims,
  `/etc/rc.conf` + `/etc/conf.d/<name>` sourcing, named runlevel
  dispatch, init.d + LSB auto-detection. Companion binaries:
  `slinit-seedrng`, `slinit-start-stop-daemon`, `slinit-supervise-daemon`,
  `slinit-fstabinfo`, `slinit-mountinfo`, `slinit-einfo`,
  `slinit-shell-var`, `slinit-svc-value`.
- Upstart-derived: `manual`, `normal-exit`, `reload-signal`, `umask`,
  `apparmor-load` / `apparmor-switch`, `debug`, inline
  `script ... end script`, `start-on-path-*` activation, `.override`
  drop-ins, `slinitctl reset-env` / `reload-all`,
  `author` / `version` / `usage`.

**New standalone binaries.**
- `slinit-runner` — post-fork execve wrapper (LSM transitions, ambient
  caps, close-fds, arg-checking restrict-* seccomp).
- `slinit-cgtop` — top-like viewer for cgroup v2 (CPU/mem/tasks).
- `slinit-sysusers` / `slinit-tmpfiles` — declarative user + runtime
  path bootstrap.
- `slinit-binfmt` / `slinit-sysctl` — systemd-* companion tool clones.
- `slinit-resource` — OCF Pacemaker resource agent (shell) for
  slinit-managed services.

**Documentation.**
- Man-page sweep: every companion binary shipped ships with a
  pandoc-formatted man page (`% TITLE(N) slinit | Sunlight Linux`
  metadata block, `go tool md2man` toolchain, no external pandoc
  dependency).
- Cross-doc consistency pass: README, CLAUDE, CONTRIBUTING, SECURITY,
  EXAMPLES, demo/README, tests/**/README all aligned on the same
  counts and same protocol version.

### Known limitations preserved into v2.0

- `starts-on-console` is only exercisable end-to-end with a real
  console arbiter — the QEMU minimal fixture can't provide one;
  covered by acceptance case 187 on live hardware.
- Intel Meteor Lake × KVM `-cpu host` × PREEMPT-RT / lowlatency host
  kernels can produce guest kernel oopses; `-cpu kvm64` sidesteps
  the issue. Documented in `demo/README.md` and
  `tests/functional/README.md`.

## Pre-v1.10.55

Detailed history for the 1.x line lives in git; walk it with:

```
git log v1.10.55
```

Milestones worth naming (drawn from the Roadmap section of `README.md`):

- **Phases 1–5**: foundation (types, state machine, config parser,
  event loop), process services, full dependency graph, control
  protocol + `slinitctl` CLI, PID 1 mode + shutdown sequence.
- **Phases 6–12**: catlog, reload, ready-notification, socket
  activation, container mode, protocol v5, push notifications, full
  dinit parity closure.
- **Phases 13–19**: runit-inspired feature bundle, `/etc/init.d`
  auto-detect, shutdown-info escalation, multi-service shared logger,
  virtual TTY, s6-linux-init parity, OpenRC UX compat.
- **Phase 20**: telco-readiness — hardware watchdog kicker, OCF
  Pacemaker resource agent, operator-intent snapshot across
  soft-reboot.
- **Phases 21–26**: upstart-derived adaptations, path activation,
  `.override` drop-ins, `script ... end script`, AppArmor
  confinement, developer stop.
- **Phases 27–40**: systemd-adaptation series — auto-managed service
  directories, filesystem sandbox cluster, seccomp filter, hardening
  cluster, start predicates, appliance basics, pre/post-start hooks,
  log-pipeline filters, credentials framework, calendar timers,
  dynamic users, file-descriptor store, services-dir auto-watch.

Sunlight OS is the primary integration target.
