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
