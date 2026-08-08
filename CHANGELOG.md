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

## [2.1.8] — 2026-08-08

Closes the systemd journalctl parity project. Nine additional flags
land across the remaining implementable groups; coverage climbs from
47/65 (v2.1.7) to **56/65 (~86%)**. The nine outstanding flags are
all systemd-specific concepts that don't map onto slinit's model
and stay unimplemented by design:

- `--image=PATH` / `--image-policy=POLICY` — need systemd's disk
  dissection library (LUKS + LVM + partitioning walk).
- `--namespace=NS` / `--list-namespaces` — slinit has one journal
  per socket, one daemon; no namespace concept.
- `--flush` / `--relinquish-var` / `--smart-relinquish-var` —
  volatile-to-persistent switching machinery that slinit's fallback
  sink doesn't need (writes straight to `/var/log/slinit-journal`
  or, if unwritable, degrades to tmpfs on startup — no runtime
  handoff).
- `--synchronize-on-exit` — libsystemd `sd_journal_close`
  configuration knob; N/A for CLI-only slinit-journalctl.
- `--force` — modifier for `--setup-keys`; slinit's `--setup-keys`
  already overwrites unconditionally (see the `SaveFSSKey` docstring
  on why one call is enough).

### Added — Group C (FSS operator surface, 3 flags)

- `--setup-keys` — mint a fresh FSS sealing key via
  `journalbin.NewFSSKey`, save to `--fss-key` path (default
  `/etc/slinit/journal-key`), print the base64 verification token
  for out-of-band sharing.
- `--verify-key=TOKEN` — inline verification token, alternative to
  the `--fss-key` file path (verifier host doesn't need a disk
  copy).
- `--interval=DUR` — epoch duration for `--setup-keys` (default 15m,
  matching systemd).

### Added — Group D (message catalog, 4 flags + new pkg/catalog)

New `pkg/catalog` implements a systemd-compatible catalog file
parser (`-- MESSAGE_ID` header + RFC 822 body). ID normalisation
strips dashes and lowercases; header keys title-case per RFC.
Compiled cache is gob-encoded to
`/var/lib/slinit/catalog/catalog.compiled` for O(1) `--dump` on
large catalogs.

- `-x` / `--catalog` — augment MESSAGE output with matching catalog
  body under the short-format line, indented two spaces.
- `--dump-catalog` — print every entry, sorted by ID.
- `--list-catalog` — print just the IDs, sorted.
- `--update-catalog` — rescan source dirs
  (`/usr/share/slinit-catalog`, `/usr/lib/slinit/catalog`,
  `/usr/lib/systemd/catalog` — with `--root` prefix), rebuild the
  cache.

### Added — Group E (invocation tracking, 2 flags + pkg/service emit)

`pkg/service` mints a 128-bit hex invocation ID
(`crypto/rand` → hex) at each `initiateStart`, stored on the
`ServiceRecord` and attached as `SLINIT_INVOCATION_ID` to every
journal event emitted during the invocation's lifecycle (Starting →
Started → Stopping → Stopped and any Failed variants).

- `--invocation=UUID` — filter events by exact
  `SLINIT_INVOCATION_ID` match. Wired through the wire filter (new
  `InvocationID` field on `QueryFilter` +
  `JournalQueryRequest`) with the same server-side push-down +
  client-fallback pattern Group A introduced.
- `--list-invocations` — requires `-u UNIT`; projects events to
  `(id, first_ts, last_ts)`, sorts by first-seen, prints one row
  per invocation. Under a daemon vintage that doesn't emit the
  field, a friendly no-invocations message points the operator at
  the emitter-side requirement.

### Wire changes

- `QueryFilter`, `JournalQueryRequest`, and `QueryFilter.isEmpty`
  gain `InvocationID`.
- Client-side `wireLimitFor` bypasses server `-n` when the new
  filter is populated (same rationale as the v2.1.7 fix).

## [2.1.7] — 2026-08-07

Patch release. Fixes a correctness regression in the v2.1.6 Group A
landing surfaced by live smoke on ceres.

### Fixed

- `journalctl`: `-t IDENT`, `-T IDENT`, and `-g PATTERN` returned an
  empty result set when combined with a small `-n` limit (e.g.
  `-t getty-tty1 -n 1` on a buffer known to contain the entry). Two
  causes, both closed in `68cafa1`:
  - `QueryFilter.isEmpty()` in `pkg/journal/buffer.go` didn't know
    about the Group A dimensions (`Identifiers`,
    `ExcludeIdentifiers`, `GrepPattern`), so a query with only
    those set took the server-side fast path — return the whole
    snapshot, trim to `Limit` — and dropped the matching events on
    the floor before the client could see them.
  - Even with the server-side fix in place, an older daemon
    vintage that predates Group A would still ignore the new JSON
    keys and apply its own `-n` trim first, reproducing the same
    symptom. The client now sends `Limit=0` to the daemon
    whenever any Group A filter is populated and applies `-n`
    locally after `clientSideFilter`, so filtering works against
    any daemon version at the cost of one extra pass over the
    returned event set.

## [2.1.6] — 2026-08-07

Systemd journalctl parity push. Slinit-journalctl started at 21 flags
against systemd's 65 (~32%); this cut brings us to 51/65 (~78%) in
two batched groups. Group A landed the client-side query + display
surface; Group B closed the maintenance ops that need daemon +
filesystem coordination. Remaining gap is Groups C (FSS, 3), D
(catalog, 4), E (invocation, 2) plus 9 systemd-specific concepts
that don't map onto slinit's model (image dissection, journal
namespaces, persistent/volatile switching).

### Added — Group A (25 flags, 950b15f)

Display modifiers: `--no-hostname`, `--utc`, `--truncate-newline`,
`--no-full`, `-l/--full`, `-a/--all`, `--no-tail`, `-e/--pager-end`,
`-q/--quiet`, `--output-fields=A,B,C`, `-m/--merge`.

Filtering: `-t/--identifier=I` (SYSLOG_IDENTIFIER include),
`-T/--exclude-identifier=I` (inverse), `--facility=NAME|N` (parsed +
warned — slinit's Event schema doesn't record facility yet),
`-g/--grep=REGEX` (RE2 on MESSAGE), `--case-sensitive[=BOOL]`
(overrides systemd's all-lowercase auto-heuristic), `--this-boot`
(alias for `--boot=0`), `-U/--user-unit=NAME` (user-scope + forces
`--user`).

Cursor / source: `--after-cursor=TOKEN` (strictly-after semantics;
`-c` becomes inclusive-at per systemd), `--cursor-file=FILE` (load +
atomic tmp+rename persist), `-D/--directory=DIR` (glob every
`*.jsonl` / `*.jsonl.gz` / `*.slj` under DIR), `--root=PATH`
(filesystem-root prefix for `--directory`, `--disk-usage` default).

Introspection (short-circuit — no event stream):
`-F/--field=NAME` (distinct values), `--fields` (list of known field
names), `--header` (metadata: file header for `--file`, buffer
summary otherwise), `--disk-usage` (bytes on disk).

Wire additions: `JournalQueryRequest` gains `Identifiers`,
`ExcludeIdentifiers`, `GrepPattern`, `GrepInsensitive` (all
`omitempty` — older daemons ignore cleanly). Client-side re-runs the
filter locally after receiving events, so `-t/-T/-g` work against
any daemon vintage — server-side pushdown is an optimization, not a
correctness dependency.

### Added — Group B (5 flags + PID file signalling, 5c63160)

Maintenance ops that need daemon coordination:

- `--sync` — force fsync of the active sink via SIGUSR1 to
  slinit-journald. Falls back to walking the journal dir + `fsync`
  per file when no daemon is running, so shutdown scripts on fresh
  systems don't hard-fail.
- `--rotate` — close current file, rename with nanosecond suffix,
  open a new one (SIGUSR2). Daemon-only — file-level rename would
  race live writes.
- `--vacuum-size=SIZE` / `--vacuum-files=N` / `--vacuum-time=TIME` —
  in-process `journald.Vacuum` with the current dated file excluded
  from deletion so a live daemon never sees its writer disappear.
  Works with or without a running daemon.
- `--pid-file=PATH` — override the default
  `/run/slinit-journald.pid` lookup path.

Wire additions:
- `journald.FileSink` / `BinarySink` gain public `Rotate()`;
  `BinarySink` gains public `Flush()` (`FileSink` already had one).
  Both extracted into a shared `rotateLocked` helper.
- `cmd/slinit-journald` writes `/run/slinit-journald.pid` at startup
  (removed on clean shutdown) and installs SIGUSR1/SIGUSR2 handlers
  via type assertion — `StdoutSink` and future sinks without
  Flush/Rotate methods remain valid without carrying no-ops.

Size / duration parsers accept systemd forms (`100M`, `2GiB`, `30d`,
`6M`, `1y`) alongside Go-native (`1h30m`, `250ms`).

Missing journal directory is a benign no-op for `--sync` and
`--vacuum-*` rather than a hard error.

### Fixed

- `--cursor` semantics now match systemd (inclusive-at); the previous
  strictly-after behavior moved to `--after-cursor` where it belongs.

## [2.1.5] — 2026-08-07

Follow-up to the v2.1.4 converter cut. Real-world validation on ceres
(46 void services under `/etc/sv/*`) showed `slinit-runit-convert`
needed operator hand-editing for the log/finish/check/down auxiliaries
and dropped `sv check DEP` on the floor. Close the gaps so every runit
sv dir round-trips through `slinit-check` cleanly with no manual
review pass. Before: 46 outputs, 25 failed lint. After: 46/46 clean.

### Fixed

- **`slinit-runit-convert`: 1:1 conversion, no manual review needed.**
  - `sv check DEP` in run script now auto-emits `waits-for: DEP`
    (previously a NOTE with "safer to review" hedge, and — before the
    intra-session /bin/sh wrap fix — a silently-dropped runtime
    dependency that let elogind start before dbus was ready).
  - `./finish` auto-wires as `finish-command = /bin/sh <path>`.
    slinit's `execFinishCommand` appends exitCode + signalNum after
    the configured argv, so the wrapped script receives runit-
    compatible `$1` / `$2`.
  - `./check` auto-wires as `ready-check-command = /bin/sh <path>`.
  - `./down` file → `manual = yes`.
  - `./log/run` recursively converts into a `<name>-log` companion
    service with `consumer-of = <name>`; primary gains
    `log-type = pipe` so slinit's consumer-attach validator accepts
    the pairing. Log companion inherits the same aux-file semantics
    (finish/check/conf on the log/ subdir all handled).
  - Default `working-dir = <svdir>` matches `runsv`'s pre-exec chdir
    — required for agetty finish's `${PWD##*-}` idiom and for
    wrapped run scripts that source `./conf` relatively.
  - `env-file` only emitted when the file actually exists on disk.
    void guards `. ./conf` with `[ -r conf ]`, so a missing conf is
    legal at runtime; slinit's env-file directive is unconditional
    and would warn under `slinit-check` on the same input.
  - Extracted bare commands now go through `exec.LookPath`, so
    `chpst -u nobody nanoklogd` becomes
    `command = /usr/bin/nanoklogd`. slinit's execve path does no
    PATH search, so a bare name would ENOENT at start.
  - Regression tests cover aux-file detection, log companion +
    `log-type = pipe` pairing, env-file existence gating, PATH
    resolution, and `sv check` → `waits-for` extraction.
  - Landed as `d7e12eb`.

## [2.1.4] — 2026-08-06

Migration acceleration cut. Three new converters land under `cmd/`
so operators can port existing service files onto slinit without
hand-editing everything. All three follow the same pattern: parse
the source format's grammar, extract into `slinitConfig`, emit a
dinit-compatible service file, WARN on anything without a 1:1
mapping so review is auditable.

### Added

- **`slinit-runit-convert`** — reads a runit service directory
  (`/etc/sv/<name>/`) and emits a slinit service file. Handles
  the void-linux convention (sourced `conf` file) and all 25
  chpst flags. Simple `run` scripts get their daemon extracted
  directly (`exec chpst -u nobody daemon` → `run-as = nobody`,
  `command = daemon`); anything with shell metachars, setup
  logic, or complex substitution falls back to
  `command = /bin/sh <sv-dir>/run` so the original stays
  authoritative. `finish`, `conf`, `down`, `log/run`, `check`,
  `control/*` all auto-detected with appropriate WARN/NOTE.
  `--enable-map` scans `/var/service/<name>` symlinks and prints
  suggested `slinitctl enable` commands. Validated against real
  void `run` scripts (3proxy, FreeRADIUS, cronie,
  GCP-Guest-Initialization).

- **`slinit-openrc-convert`** — reads an OpenRC `init.d` script
  and emits a slinit service file. Two paths: (1) variable-only
  scripts (`command=`, `pidfile=`, `depend()`, no custom
  `start()`/`stop()`) get a self-contained slinit file with no
  runtime openrc-run dependency — 5/63 scripts in the OpenRC
  tree fit this shape. (2) Scripts with custom shell functions
  (58/63, the common case) get wrapped as
  `command = /usr/sbin/openrc-run <script> start`, preserving
  every ebegin/einfo/start-stop-daemon call. `--wrapper=` swaps
  the invocation for slinit-openrc-shim variants.
  `depend()` verbs map: `need` → `depends-on:`, `use`/`after` →
  `waits-for:`; `before` warns (invert on the target),
  `provide` and `keyword` note the semantic gap. Auto-detects
  `/etc/conf.d/<name>` as env-file. `--enable-map` scans
  `/etc/runlevels/*/<name>`.

- **`slinit-systemd-convert`** — reads a `.service` unit and
  emits a slinit service file. Section-aware INI parser handles
  `\`-line-continuation. About 40 [Unit] + [Service] + [Install]
  directives mapped, everything else warned so the operator sees
  what's unrepresented. Type= maps as
  simple/exec→process, forking→bgprocess, oneshot→scripted,
  notify/notify-reload→process (with a note about notify-fd).
  ExecStart prefix chars (`-+!:@`) stripped with a NOTE per
  prefix; multiple ExecStartPre/Post lines warn (slinit takes
  one). Restart= collapses systemd's 5 values into slinit's 3
  (no/yes/on-failure) with ambiguous cases warned.
  User+Group merge into `run-as = user:group`. Hardening
  directives (Private*, Protect*, Restrict*, SystemCall*)
  produce NOTEs naming the equivalent slinit directive so the
  operator can add them by hand. Dep names normalise: strips
  `.service`, `.target`, `.socket`, `.path`, `.mount`, `.timer`,
  `.swap`, `.device` so slinit sees bare names. Rejects timer /
  socket / path / mount / target units at the guard — those need
  slinit-native equivalents, not mechanical translation.
  Template units (`@` in the name) also rejected — instantiate
  first.

All three tools:
  * Are single-file cmds (~350–500 LOC each) plus table-driven
    tests (5–10 test funcs, 30–80 assertions each);
  * Share a `--dry-run` / `--verbose` / `--output-dir=DIR` flag
    surface for muscle-memory consistency (runit and openrc add
    `--enable-map` for their respective enable markers);
  * Emit a `# Converted from <path>` provenance comment at the
    top of every output so downstream review has a clear source.

## [2.1.2] — 2026-08-04

Recovery + boot refactor: brings slinit's kernel-cmdline surface,
rescue/emergency semantics, and boot-timing tooling to systemd-analyze
parity (four of five subcommands, per-svc self-time annotated) while
keeping slinit's unique interactive UX (Ctrl-B live debugger, boxed
rescue menus, boot-collapse dialog). Ten features shipped across six
planned phases + a dozen follow-up UX fixes from live QEMU testing.

### Added

- **`pkg/bootmode` — structured kernel-cmdline boot-mode parser**
  (Phase 1). Centralises what were scattered `kcmdlineHasFlag` calls
  in `cmd/slinit/main.go` into a single `bootmode.Options` struct
  with typed fields for the full slinit + systemd operator surface:
  `Mode` (Normal / Emergency / Rescue), `DebugShell`, `ConfirmSpawn`,
  `CrashShell`, `LogLevel`, and legacy `Debug`. Recognized tokens:

  - Bare: `single`, `s`, `1` → Rescue (sysvinit runlevel 1 compat);
    `emergency` → Emergency; `rescue` → Rescue;
    `slinit.emergency`, `slinit.rescue`, `slinit.debug-shell`,
    `slinit.confirm-spawn`, `slinit.crash-shell`, `slinit.debug`.
  - Key=value: `slinit.log-level=<lvl>`.

  Last-mode-wins on conflicts (`emergency rescue` → Rescue), matching
  systemd precedence. KEY=VALUE forms of bare-token selectors are
  ignored so `single=1` cannot accidentally trip Rescue. 34-case
  test table exercises the full grammar.

- **Emergency vs Rescue split** (Phase 2, systemd rescue.target /
  emergency.target parity). Rescue runs `mount -a` first so
  `/etc/fstab` is honoured (operator has `/home`, `/var`, `/tmp`
  before the sulogin). Emergency stays filesystem-agnostic so it
  works even when fstab is broken or a critical mount hangs. New
  `mountLocalFsBestEffort`: 30s context timeout guards a hanging
  NFS/iSCSI mount, best-effort semantics keep the shell reachable
  regardless of exit, silent-skip when `mount(8)` is absent.

- **Persistent debug shell on /dev/tty9** (Phase 3, systemd
  `debug-shell.service` parity). Enabled by `slinit.debug-shell`
  on the kernel cmdline. An always-on root shell on a dedicated VT
  that never competes with getty on `/dev/console`. Solves the
  post-boot debug-access problem architecturally — previously
  planned via SIGUSR1 + `slinitctl debug` (Phase 2 of the old
  debugger TODO), which needed pty interposition or a getty shim.
  Respawn loop with getty-style crash-loop guard.

- **`slinit.confirm-spawn`** (Phase 5, systemd `confirm_spawn`
  parity). Kernel cmdline installs a `ServiceSet.OnConfirmSpawn`
  hook that prompts `start service X? [Y/n]` on `/dev/console`
  before every service activation. Gated at `ServiceRecord.callBringUp`
  (the single call site every service type flows through — not just
  ProcessService's `startProcess`) so InternalService,
  TriggeredService, BGProcessService, and ScriptedService all
  prompt too. Cbreak mode: one keypress dispatches (no Enter).
  Mutually exclusive with the boot debugger — both need exclusive
  read on `/dev/console`.

- **`slinit.crash-shell`** (Phase 5, systemd `crash_shell` parity).
  Drops into sulogin on `/dev/console` when PID 1 panics, BEFORE
  the existing kill-all + emergency-reboot path fires. Best-effort:
  no sulogin found or `/dev/console` un-openable falls through to
  the normal emergency reboot. New package-level
  `pkg/shutdown.CrashPauseFn` wired to `serviceSet.SetCrashPause`
  freezes the state machine (gated at both `callBringUp` and
  `startProcess` to catch the smooth-recovery path too) so a
  restart=yes tty svc cannot respawn its shell while sulogin holds
  the tty. Existing tty owners get `SIGKILL` before the reopen —
  `SIGHUP` was tried first but bash on Alpine (through some
  interaction with Go runtime signal masking) refused to die.
  Goroutine panics inside slinit (event loop, control server,
  rescue-shell, debug-shell respawn) each wrap themselves with
  `defer shutdown.CrashRecovery` — a bare goroutine panic in Go
  crashes the whole process, main's own defer never sees it.

- **`slinitctl analyze` subcommand dispatcher** — extends the
  historical `slinitctl boot-time` (still an alias) with
  systemd-analyze-style sub-commands:

  - `analyze time` / `blame` — the existing kernel+userspace
    summary + per-svc blame output (backwards-compatible default).
  - `analyze critical-chain [SVC]` — walks the dep graph from the
    boot service backwards (memoized DFS), showing the longest
    chain with per-node inclusive duration + self time. Self-time
    is `parent_dur - max_child_dur`, the operator's answer to
    "who actually did work?" vs "who waited?". Live demo boot
    surfaces `chain-a` as the real bottleneck (+2.079s self out
    of 2.112s inclusive).
  - `analyze dot` — reuses existing `cmdGraph` (Graphviz DOT).
  - `analyze plot` — stub with a helpful error message. SVG
    timeline layout needs per-svc start timestamps but the
    BootTime protocol only exposes StartupNs durations; extending
    the protocol is a follow-up.

  New helper `fetchDepGraph` replays cmdGraph's list + FindService
  + QueryDependencies rounds and returns an adjacency map for
  programmatic walking. `slinitctl boot-time` untouched.

- **Test hook for crash-shell validation** (`-tags paniconce` build).
  New `cmd/slinit/panictest_on.go` (active only with the tag) arms
  a goroutine that panics after N seconds when
  `slinit.panic-after=N` is on the kernel cmdline. Never compiled
  into production slpkgs builds; the demo `build.sh` sets the tag
  so `./demo/run.sh --panic-after=5 --crash-shell` validates the
  panic → crash-shell → sulogin → emergency-reboot end-to-end.

- **Demo bootmode selector flags in `demo/run.sh`**: `--rescue`,
  `--emergency`, `--confirm-spawn`, `--crash-shell`, `--debug-shell`,
  `--debug`, `--log-level=X`, `--panic-after=N`. Composable; base
  cmdline unchanged so bare `./run.sh` behaves as before.
  `demo/build.sh` also picks up three binaries that had shipped
  in the slpkgs template but were missing from the demo initramfs:
  `slinit-logouthookd`, `slinit-sysusers`, `slinit-tmpfiles`.

### Changed

- **Rescue / emergency now keep the control socket + event loop
  alive** (systemd rescue.target parity). The initial cut bypassed
  slinit's infrastructure entirely — control socket unopened, event
  loop never started — so `slinitctl` commands from inside the
  rescue shell failed with "no such file or directory". Refactored:
  the rescue-mode gate short-circuits boot-services load + debugger
  + confirm-spawn (all irrelevant to a bare-shell boot) but still
  runs through `ctrlServer.Start` and `loop.Run`. Rescue shell is
  spawned in a goroutine that, on exit, calls
  `loop.InitiateShutdown(reboot)`. `slinitctl shutdown` from inside
  the shell now routes through the same shutdown path a normal boot
  uses.

  `pkg/eventloop.initiateShutdown` gained an empty-set fast path:
  after `StopAllServices` returns, if `CountActiveServices() == 0`
  the loop pokes `forceExitCh` immediately instead of waiting for
  the 90s emergency timer. Live QEMU: `slinitctl shutdown` from
  rescue now reboots in ~19s vs the ~107s the old timer-wait took.

- **Recovery menu rendering unified through shared box primitives**
  (Phase 6). `pkg/recovery/menu.go` now exposes `writeBoxHeader /
  writeBoxBlank / writeBoxLine / writeBoxFooter` plus the
  `menuBoxBar` constant; `Present`, `PresentCollapse`, and
  `Debugger` all render through them so box style (width, bar
  character, prompt) lands in one place. `renderServiceBlock` and
  `renderErrorBlock` in the debugger use the new primitives too.
  Behavioural output identical.

- **Boot debugger menu — five UX fixes from live QEMU testing:**

  1. `Logger.PauseBootConsole` / `ResumeBootConsole` silence the
     compact `[ OK ] name` renderer while the debugger menu is
     open, so services finishing in parallel don't shatter the
     boxed layout.
  2. Countdown-line verb is now caller-configurable — was hardcoded
     "Auto-reboot" but the debugger footer says "Auto-continue",
     the two contradicted each other.
  3. Countdown redraw switched from `time.Ticker` (which fired
     once then stopped on the demo serial console — never
     root-caused) to `time.After` per iteration; visibly ticks
     down every second now.
  4. `Debugger.Stop` waits on `menuMu` before touching the tty so
     boot completing in the background doesn't rip the menu out
     from under an operator mid-interaction.
  5. `clearPrompt` closure wipes the countdown line on every read
     return so downstream `[ OK ] tty` / dispatch logs land on a
     clean row, not stamped over `Auto-continue in Xs …`.

- **`runRescueShell` label + signal-aware exit handling.** The
  helper hardcoded a `slinit.rescue:` prefix even when called from
  Emergency mode; now takes a `label` parameter that main passes
  as `bootmode=<mode>`. SIGTERM/SIGKILL/SIGHUP exits (how
  `slinitctl shutdown` / `poweroff` from inside the shell reach us)
  downgrade from ERROR to Info — the shell being signaled during
  shutdown is normal, not an error.

- **`slinit.log-level=<lvl>` now actually applied.** The Phase 1
  parser captured the field but nothing wired it into
  `logger.SetLevel`. Fixed. Debug still wins on precedence
  (comprehensive: verbose + boot console off); LogLevel is the
  finer knob (level threshold only, boot console preserved).

- **Demo fstab uses `noauto` on `/dev/vda`/`/dev/vdb`** so
  `mount -a` from Rescue mode exits cleanly when the demo VM is
  launched without `-drive` (the default). `nofail` was tried
  first but Alpine's busybox `mount(8)` doesn't honour it
  (systemd/util-linux only); `noauto` is universal.

### Removed

- **`cmd/slinit-analyze`** — briefly landed as a standalone binary,
  then deleted the same session after a live QEMU comparison
  showed it duplicated the existing `slinitctl boot-time` (and
  `slinitctl analyze` alias) with a strictly inferior metric
  (cumulative delta from boot start vs the existing per-svc
  activation duration). The `slinitctl analyze` subcommand
  dispatcher added under **Added** above delivers the same
  systemd-analyze surface without a redundant binary.
## [2.1.1] — 2026-08-03

Point release focused on the boot-time operator UX. Third boot-failure
menu (live debugger on Ctrl-B) lands and rounds out the trio started
in [2.1.0]. Two follow-up fixes surfaced from live QEMU testing:

- **Boot debugger detach moved before console-owning service exec.**
  The initial cut stopped the debugger from `boot` EventStarted, which
  ran *after* `bash --login` had already opened `/dev/console` and
  captured slinit's raw termios as its "original" — so echo worked for
  the first command and then vanished. `ServiceSet.OnConsoleAcquire`
  now fires from `ProcessService.BringUp` right before `StartProcess`
  when `params.OnConsole` is true, giving the debugger a chance to
  release termios before the child inherits the fd. Reader loop also
  switched from a blocking `bufio.ReadByte` to `unix.Poll` with a
  200ms timeout because Linux does not unblock a pending tty read on
  `Close()` — the previous `Stop` hung waiting for a keystroke.
  Restore-termios in `Stop` re-opens `/dev/console` for the ioctl
  since the reader fd is closed by that point (per-tty state, not
  per-fd).

- **Force-fail filters aggregate services out of its target set.**
  The `[f]` action force-fails `snap.InProgress[0]`. Aggregate services
  (`boot`, `all-services` — no command, just dep bundles) show as
  STARTING until the whole tree resolves; if `[f]` hit one of those
  the cascade would take the whole dep graph down and trigger
  `BOOT COLLAPSE` — the opposite of what an operator hitting force-fail
  on a stuck child wants. Split by PID in main's `StatusFn`: process
  svcs go to In-progress (targetable), aggregates go to Waiting on deps
  (visible but not targetable). Verified end-to-end in QEMU: `[f]` on
  a stuck child no longer nukes the tree.

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

- **Interactive boot debugger** (`recovery.Debugger`). A raw-mode
  reader on `/dev/console` that pops a live-status menu on Ctrl-B
  during boot. Third sibling of the two boot-failure menus, sharing
  the same visual language and single-keypress + Ctrl-B/Ctrl-D
  conventions. Wired in `cmd/slinit/main.go` right before the boot
  service loop; auto-detaches when the boot service reaches STARTED
  (login prompts are up → getty owns `/dev/console` → further reads
  from us would compete with login input). Menu content:
  - Live snapshot: in-progress services with PIDs, and any waiting
    ones — recomputed each time the menu opens
  - `[c]` / Ctrl-D — continue (dismiss, resume listening)
  - `[s]` / Ctrl-B — drop to shell (canonical mode restored around
    the fork; re-arms raw on shell exit, then re-presents the menu)
  - `[f]` — force-fail the first in-progress service (invokes
    `ServiceSet.ForceStopService`; useful for skipping a stuck dep
    without a full reboot)
  - `[r]` — reboot / `[p]` — poweroff
  - Auto-continue after 60s (menu doesn't strand a headless system)

  Honest scope: the debugger does NOT freeze slinit's event loop —
  that would deadlock the watchdog feeder + control-socket accept +
  signal handling. What it does is present a LIVE-STATUS view; the
  state machine keeps running underneath while the menu is open.
  Force-fail is the only action that mutates state.

  Phase 1 scope is boot-time (physical Ctrl-B on `/dev/console` up
  to getty-start). Post-boot access via `slinitctl debug` + SIGUSR1
  is a Phase 2 follow-up — physical Ctrl-B post-boot would require
  pty interposition (~800 LOC) or a getty-shim binary and risks
  breaking the login flow, so the signal-based path is the cleaner
  answer for "listens always" semantics after boot.

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
