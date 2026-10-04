# slinit development history and roadmap

Mostly history: the numbered phases below are the build-out of slinit
from a dinit port to its current surface, and they run to **v2.3.9**.
Everything after that — v2.4 through the current release — is in
[CHANGELOG.md](../CHANGELOG.md), which is the authoritative per-release
record and is not duplicated here. [STABILITY.md](../STABILITY.md) is
what slinit promises not to break.

## Towards 3.0

The 3.0 list was six items. Five are closed:

1. **A skipped functional case reported PASS** — and in fact the harness
   was reporting *every* case as PASS. Fixed in v2.7.1, with
   `tests/functional/selftest.sh` to keep it honest.
2. **The dependency graph was mutated without a lock** from every
   control connection — nine sites, readers included.
3. **The path-activation stall**: an arm-time trigger firing inside the
   loader's service-loaded hook, so the start it triggered raced the
   load. Root cause was (2); a watchdog now dumps goroutines to
   */run/slinit-stall.stack* if the scheduling lock is ever held too
   long.
4. **The status structures were filled carelessly** — a service whose
   command did not exist reported that it stopped *normally*. Fixed in
   v2.7.2.
5. **The logrotate EBADF flake**: one discarded `os.NewFile(w.Fd())` in
   a test gave one descriptor two owners, and the orphan's finalizer
   closed another test's file. `pkg/features/fdownership_test.go` now
   fails the build on that pattern.

What 3.0 does **not** include:

* **Packaging and the real-hardware proving ground.** The downstream
  package recipe trails the repo, and "proves slinit as PID 1" deserves
  a current ISO booted on real hardware rather than test counts alone.
  That is the first thing after the cut, not part of it.

`slinitctl add-dep`'s argument order was the other open item, and it is
settled in 3.0.0: both orders work, since the dependency-type names are a
closed set and the position holding one of them is unambiguous. Nothing
breaks, and dinit's order is accepted again.

`pam_slinit.so` is scheduled for 3.0.1 rather than 3.0: a PAM module is
a C shared object, so building one puts the Go runtime into every
forking login process, and that trade needs its own release to get
wrong in.

## Phase history


- [x] **Phase 1**: Foundation -- types, state machine, config parser, event loop
- [x] **Phase 2**: Process services -- fork/exec, child monitoring, restart logic
- [x] **Phase 3**: Full dependency graph -- all 6 dep types, TriggeredService, BGProcessService
- [x] **Phase 4**: Control protocol + `slinitctl` CLI
- [x] **Phase 5**: PID 1 mode + shutdown sequence
- [x] **Phase 6**: Advanced features -- catlog, reload, ready-notification, socket activation, provides, unload, consumer-of, logfile, wake/release, is-started/is-failed, setenv/getallenv, add-dep/rm-dep, enable/disable, nice/oom/ioprio/cgroup/rlimits, capabilities/securebits, unmask-intr, auto-recovery, starts-rwfs/starts-log, pass-cs-fd, kexec
- [x] **Phase 7**: Container mode, env substitution, shutdown hooks, UTMPX
- [x] **Phase 8**: CLI flags batch -- unpin, softreboot, --system/-s/--user/-u, --no-wait/--pin/--force, --ignore-unstarted, --offline/-o/--services-dir/-d, --use-passed-cfd/--from, multiple default service dirs
- [x] **Phase 9**: Daemon flags -- --system-mgr, --env-file, --ready-fd, --log-file, --cgroup-path, SLINIT_SERVICENAME/SLINIT_SERVICEDSCDIR, advanced env substitution, command +=, load-options, kernel cmdline filtering
- [x] **Phase 10**: Push notifications -- SERVICEEVENT, LISTENENV/ENVEVENT, mutex-serialized writes
- [x] **Phase 11**: Protocol v5 -- LISTSERVICES5/SERVICESTATUS5/SERVICEEVENT5, slinit-check, slinit-monitor, @include/@include-opt
- [x] **Phase 12**: Complete dinit parity -- @meta, env-file meta-commands, PINNEDSTOPPED/PINNEDSTARTED, SERVICE_DESC_ERR/SERVICE_LOAD_ERR, PREACK, QUERY_LOAD_MECH, DEPENDENTS, $/NAME word-splitting, service templates (name@arg), @meta enable-via, SIGUSR1 socket reopen, soft-reboot shutdown hooks
- [x] **Phase 13**: Runit-inspired features -- finish-command, ready-check-command, pre-stop-hook, env-dir, control-command (custom signal handlers), chroot, new-session, lock-file, close-fds, pause/continue, log rotation (size/time/max-files), log filtering (include/exclude regex), log processor, down-file marker, once command
- [x] **Phase 14**: /etc/init.d auto-detect with LSB header parsing, BSD rc.d support
- [x] **Phase 15**: Shutdown info display, escalating force shutdown, cron-like periodic tasks, soft parallel start limit, proper socket activation (multiple sockets, TCP/UDP, on-demand)
- [x] **Phase 16**: Multi-service shared logger (SharedLogMux -- N producers → single logger, line-prefixed)
- [x] **Phase 17**: Virtual TTY -- screen-like attach/detach via PTY allocation, ring buffer scrollback, `slinitctl attach`
- [x] **Phase 18**: s6-linux-init parity -- catch-all logger, TAI64N/ISO/none timestamps, scheduled shutdown + cancel + status, wall broadcasts, `/etc/shutdown.allow` access control, configurable grace, global rlimits, RT-signal container shutdown (SIGRTMIN+3..+6), UTMPX logout + wtmp RUN_LVL shutdown, kernel cmdline snapshot, `/run` tmpfs run-mode, configurable devtmpfs, SysV argv[0] compat (`halt`/`poweroff`/`reboot`), `slinit-init-maker`, `slinit-nuke`
- [x] **Phase 19**: OpenRC UX compat -- `rc-service`/`rc-update`/`rc-status` argv shims, `/etc/rc.conf` + `/etc/conf.d/<name>` sourcing via `sh -c` wrapper, runlevels modelled as `runlevel-<name>` services, named-runlevel dispatch (`init default|single|nonetwork|boot|sysinit`)
- [x] **Phase 20**: Telco-readiness -- hardware watchdog (`/dev/watchdog0` kicker with magic-close disarm), Pacemaker OCF resource agent for slinit services, operator-intent snapshot persisted across soft-reboot, /run remount semantics, catch-all logger consistency between PID 1 and soft-reboot, escalating force-shutdown surfacing blockers
- [x] **Phase 21**: Upstart-derived adaptations -- `manual` stanza (opt-in services), `normal-exit` (declared success exit codes/signals), `reload-signal` (declarative SIGHUP-style reload + `slinitctl reload-signal`), `reload-all` (bulk rescan), `reset-env` (clear per-service runtime env), per-service `umask`, `author`/`version`/`usage` metadata stanzas surfaced by `slinitctl status`; service-watchdog timeout now respects `restart=on-failure` / `restart=yes` policy
- [x] **Phase 22**: Path-based activation -- inotify-driven `start-on-path-exists` / `start-on-path-changed` / `start-on-path-modified` / `start-on-directory-not-empty` stanzas with systemd-style one-shot semantics (re-armed on `EventStopped`); single global watcher serves all services via the new `pkg/pathwatch` package
- [x] **Phase 23**: Upstart-style `.override` drop-ins -- a sibling `<service>.override` file modifies a packaged service's stanzas (scalars replace, `+=` appends) without editing the shipped file; parsed after conf.d overlays so it has the final say, with template `$1` substitution preserved
- [x] **Phase 24**: Upstart-style `script ... end script` inline shell -- a verbatim multi-line block becomes the service command via `/bin/sh -c`; pure sugar over `command` (same load-time `$VAR`/`$1` substitution, mutually exclusive with it), unterminated block is a fatal parse error
- [x] **Phase 25**: AppArmor confinement (first LSM integration) -- `apparmor-load` parses a profile (`apparmor_parser -r`) parent-side before start; `apparmor-switch` transitions on exec via `slinit-runner` writing `/proc/self/attr/exec` (`aa_change_onexec`), since the kernel binds the transition to the task performing the `execve`; both fail closed
- [x] **Phase 26**: `debug = yes` developer stop -- slinit-runner raises `SIGSTOP` after runner setup but before `execve` so `gdb -p` can attach pre-exec; `kill -CONT` resumes into the AppArmor transition + exec. Completes the upstart-feature adaptation backlog (all 12 candidates shipped)
- [x] **Phase 27**: systemd-style auto-managed service directories -- `runtime-directory`/`state-directory`/`cache-directory`/`logs-directory`/`configuration-directory` (+`-mode`) created and chowned to `run-as` under `/run`,`/var/lib`,`/var/cache`,`/var/log`,`/etc` before start; runtime dir removed on stop per `runtime-directory-preserve` (no/yes/restart). First item of the systemd-adaptation backlog
- [x] **Phase 28**: Filesystem sandbox cluster -- `private-tmp`, `protect-system`, `read-only-paths`/`read-write-paths`, `protect-home`, `inaccessible-paths`, `protect-proc`/`proc-subset`, `bind-paths`/`bind-read-only-paths`, `temporary-filesystem`; applied child-side via `slinit-runner` in a fresh mount namespace (MS_PRIVATE on `/`, then layered overlays)
- [x] **Phase 29**: Seccomp filter -- `system-call-filter` with `~deny` prefix + curated `@group` allowlists (`@system-service`, `@privileged`, `@network-io`, ...), `system-call-architectures`, `system-call-error-number`, `system-call-log`; cBPF compiler in `pkg/seccomp` (multi-arch syscall tables, native + extra arches)
- [x] **Phase 30**: Hardening cluster (`Restrict*`/`Protect*` v1) -- `protect-kernel-tunables/-modules/-logs/-clock/-control-groups/-hostname`, `lock-personality`; mount-based knobs (RO `/proc/sys`,`/sys/fs/cgroup`) compose with mount-namespace setup, seccomp-based knobs (`clock_settime`,`sethostname`,`personality`,`init_module`...) feed into the runner's seccomp install
- [x] **Phase 31**: Dinit upstream parity refresh -- `prepared-by` dependency type (hard dep that also restarts when the dependent restarts), enable/disable persisted via `waits-for.d` symlink, `slinit-check` validates `consumer-of` (producer exists / right type / `log-type=pipe`); restart-limit-count entering a stable FAILED state instead of looping `initiateStart -> exit -> Stopped -> initiateStart`
- [x] **Phase 32**: Start predicates -- `condition-*` (skip silently on fail) and `assert-*` (fail start) with `!` negation; 13 kinds (`path-exists`/`-glob`, `path-is-directory`/`-mount-point`, `file-not-empty`, `directory-not-empty`, `kernel-command-line`, `virtualization`, `first-boot`, `host`, `security`, `needs-update`, `ac-power`); SKIPPED short-circuits to STARTED so dependents proceed
- [x] **Phase 33**: Appliance basics -- `failure-action` / `success-action` (none/reboot/poweroff/halt/exit) + `reboot-argument`; `runtime-max-sec` (hard cap on STARTED time, stop via normal path); `oom-policy` (continue/stop/kill) driven by a per-service cgroup-v2 `memory.events` watcher
- [x] **Phase 34**: Pre-start / post-start hooks -- systemd-style `pre-start-command` (sync, non-zero exit fails start) and `post-start-command` (async, log-only); same working-dir / env / timeout as `finish-command`
- [x] **Phase 35**: Log pipeline filters -- `log-rate-limit-interval`/`log-rate-limit-burst` (token bucket; drops with a single "limit hit" notice) + `log-level-max` (syslog `<N>` priority gate, lines without prefix treated as info)
- [x] **Phase 36**: Credentials framework -- `load-credential=NAME:PATH` (copy file) and `set-credential=NAME:VALUE` (inline); materialised at `/run/credentials/<svc>/` on a fresh ro tmpfs (`size=1M`, `mode=0700`, `MS_NOSUID|MS_NODEV|MS_NOEXEC`), files `0400` chowned to run-as, `$CREDENTIALS_DIRECTORY` exported to the service. No env leakage via `/proc`.
- [x] **Phase 37**: Calendar timers -- `cron-calendar` (`daily`, `hourly`, `weekly`, `Mon..Fri 09:00`, `Mon,Wed,Fri 12:00`, `*-*-1 00:00`, `*:0/15`); `cron-randomized-delay` jitter; `cron-persistent` catch-up; per-field advancement in NextAfter (no brute-force second iteration)
- [x] **Phase 38**: Dynamic users -- `dynamic-user=yes` allocates a transient UID/GID from a per-daemon pool (61184..65519, matching systemd) at every BringUp via shared `UIDPool`; released in Stopped(); no `/etc/passwd` entry. UID-dependent setup (`runtime-directory`, `credentials`) sees the same transient identity
- [x] **Phase 39**: File-descriptor store -- `file-descriptor-store-max=N` creates a per-service `$NOTIFY_SOCKET` Unix datagram socket at `/run/slinit/notify/<svc>.sock`; sd_notify packet parser routes `FDSTORE=1` + `FDNAME=name` with SCM_RIGHTS fds into an in-memory store; next BringUp prepends them to `LISTEN_FDS` (with names in `LISTEN_FDNAMES`). **Closes the systemd-adaptation backlog (14/14 items shipped; `#7 v2` arg-checking BPF for `RestrictRealtime`/`SUIDSGID`/`MDWE`/`Namespaces`/`AddressFamilies` deferred -- needs `pkg/seccomp` BPF compiler extension)**
- [x] **Phase 40**: Services-dir auto-watch (`--watch-services-dir`) -- opt-in `inotify(7)`-based multiplexer (`pkg/svcdirwatch`) watches every services-dir; new file → `LoadService`, removed file → `UnloadService` (only when *STOPPED*), modified file → informational log (the existing *(modified since loaded)* marker still fires via `status`). Editor artefacts (`.`, `~`, `.swp`, `.tmp`, `.new`, `.bak`) and `.d` overlay dirs are filtered; a 300 ms debounce collapses editor multi-event bursts (write + close + rename) into a single dispatch per file. Inspired by `runsvdir`'s inotify rescan (runit 2.3.1+)
- [x] **Phase 41** (v2.1.0): Journal Phase 1 -- event bus foundation. `pkg/journal.Event` schema (Ts/Mts/Msg/Prio/Unit/Fields + trusted metadata Pid/Uid/Gid/Comm/Exe/Cmdline/BootID/MachineID/Hostname), in-process `EventBuffer` ring, `Emit`/`Subscribe`/`ResolveIdentifier`, `QueryFilter` with Match. Wired into `pkg/service/journal_emit.go` so every state transition (Starting → Started → Stopping → Stopped, Failed variants) publishes a driver-transport event
- [x] **Phase 42** (v2.1.0): Journal Phase 2+3 -- query CLI + persistent daemon. `slinit-journalctl` reads via `CmdJournalQuery` (single-shot) + `CmdJournalSubscribe` (follow mode); `slinit-journald` consumes `/run/slinit/events.sock` and writes JSONL to `/var/log/slinit-journal/*.jsonl` with size + age rotation; tmpfs fallback to `/run/slinit-journal/` on unwritable primary. Kernel events read directly from `/dev/kmsg` from boot start (`-k / --dmesg` returns current-boot kmsg with `unit=kernel` + no [PID] bracket). Backlog replay: daemon queries slinit's ring buffer at startup and persists everything emitted since boot but before its socket bound
- [x] **Phase 43** (v2.1.0): Journal Phase B -- binary format (SLJRNL01 magic, 240-byte header, 7 object types DATA/FIELD/ENTRY/HASH_TABLEs/ENTRY_ARRAY/TAG, jenkins lookup3 hash) + FSS sealing via HKDF-SHA256 + HMAC-SHA256 TAG chain (per-epoch keys derived from a seed; forward-secrecy survives compromise of a sealing state at time T). `slinit-journalctl --verify --file=<binary>` walks the TAG chain against the key file. Also lands verbose + export output formats and binary-vacuum wired through the rotate hook
- [x] **Phase 44** (v2.1.0): Dinit-parity sweep -- 5 env-var + bootstrap-path gaps closed (`DINIT_SERVICE`, `DINIT_CS_FD`, `DINIT_SOCKET_PATH`, `/etc/slinit/environment` auto-load, `XDG_CONFIG_HOME` + `$HOME/.config` dedup). Dual-wire disable: `CmdDisableServiceV7=62` (slinit-native atomic) is the default; `--dinit-compat` routes through `CmdRmDepV7=30` (race-free rm-dep, dinit-compatible) for interop with a real dinit daemon. Journal render rules: `unit[PID]:` bracket shows the SUBJECT service's PID via `SLINIT_TARGET_PID` (never the emitter's PID 1); slinit-internal driver-transport events without a target PID skip the bracket entirely
- [x] **Phase 45** (v2.1.0): `slinit-supports` self-introspection CLI -- `--list-directives` / `--list-opcodes` / `--list-all` enumerations + direct lookup by name. Companion to `doc/features.md` so package managers and CI can query slinit's capability set without parsing source
- [x] **Phase 46** (v2.1.1): Interactive boot debugger -- Ctrl-B trigger during boot opens a rescue menu (cbreak tty mode, EOF from canonical-mode maps to Retry). Aggregate services filtered out of force-fail target (they can't be force-failed meaningfully); boot debugger detaches BEFORE console-owning service exec so it doesn't clobber the child's terminal
- [x] **Phase 47** (v2.1.2): Recovery + boot refactor -- Emergency vs Rescue split (Rescue keeps control socket + event loop alive so operators can debug live); tty9 debug-shell (respawn loop on kernel cmdline `slinit.debug-shell`); confirm-spawn gate at `allDepsStarted` (all 5 service types prompt with cbreak dispatch — single keypress); crash-shell end-to-end with service freeze during the drop; `bootmode` package with structured kernel-cmdline parser (`slinit.emergency`, `slinit.rescue`, `slinit.debug-shell`, `slinit.confirm-spawn`, `slinit.crash-shell`, `slinit.log-level=` wired to logger.SetLevel)
- [x] **Phase 48** (v2.1.2): `slinitctl analyze` subcommand dispatcher -- `time` (boot summary), `blame` (per-svc durations desc), `critical-chain` (slowest dep-path walk, terminates at `boot`), `dot` (GraphViz digraph with edges), and `plot` (SVG timeline; landed in v2.4.8 once the BootTime reply grew an additive tail carrying per-svc start instants). Replaces the removed `slinit-analyze` binary
- [x] **Phase 49** (v2.1.4): Migration converters -- three legacy-config → slinit converters land under `cmd/`: `slinit-runit-convert` (parses `/etc/sv/<name>/` with chpst flag extraction), `slinit-openrc-convert` (variable-only vs custom-start() dispatch to self-contained or `openrc-run`-wrapped output), `slinit-systemd-convert` (INI parser with `\`-line continuation, ~40 directive mappings). All three emit WARN/NOTE for anything without a 1:1 mapping so review is auditable
- [x] **Phase 50** (v2.1.5): runit converter 1:1 refactor -- log companion generation (`log/run` → `<name>-log` service with `consumer-of` + `log-type = pipe` on primary), auto-detection of `finish` / `check` / `down` / `conf` auxiliary files (→ `finish-command` / `ready-check-command` / `manual` / `env-file`), `sv check DEP` in run scripts auto-emits `waits-for: DEP`, `working-dir` defaults to sv dir (runsv chdir compat), bare-name commands resolved via `exec.LookPath` (slinit's execve does no PATH search). Output round-trips through `slinit-check` clean on real Void `/etc/sv/*` services (46/46 lint clean)
- [x] **Phase 51** (v2.1.6): journalctl systemd parity Groups A+B -- 30 flags land. Group A (25, client-side): `--no-hostname` / `--utc` / `--truncate-newline` / `--no-full` / `-l/--full` / `-a/--all` / `--no-tail` / `-e/--pager-end` / `-q/--quiet` / `--output-fields=A,B,C` / `-m/--merge` (display); `-t/--identifier` / `-T/--exclude-identifier` / `--facility` / `-g/--grep` / `--case-sensitive[=BOOL]` / `--this-boot` / `-U/--user-unit` (filtering); `--after-cursor` / `--cursor-file` / `-D/--directory` / `--root` (cursor+source); `-F/--field` / `--fields` / `--header` / `--disk-usage` (introspection). Group B (5, maintenance): `--sync` via SIGUSR1, `--rotate` via SIGUSR2, `--vacuum-size` / `--vacuum-files` / `--vacuum-time`. New JournalQueryRequest wire fields (Identifiers / ExcludeIdentifiers / GrepPattern / GrepInsensitive) with client-side re-filter fallback for older daemons
- [x] **Phase 52** (v2.1.7): journalctl `-t/-T/-g` + small `-n` correctness fix -- `QueryFilter.isEmpty()` learned about the Group A dimensions so filtered queries take the slow path (Match per event) and trim AFTER filtering. Client also sends `Limit=0` when a Group A filter is populated + trims locally, so filters work against any daemon vintage
- [x] **Phase 53** (v2.1.8): journalctl systemd parity Groups C+D+E -- 9 flags. Group C (FSS, 3): `--setup-keys` mints sealing key + prints verification token, `--verify-key=TOKEN` inline verification, `--interval=DUR` epoch duration. Group D (catalog, 4 + new `pkg/catalog`): systemd-compatible `.catalog` parser (ID normalization + RFC 822 headers title-cased); `-x/--catalog` augments output, `--dump-catalog`, `--list-catalog`, `--update-catalog` (gob-compiled cache at `/var/lib/slinit/catalog/catalog.compiled`). Group E (invocation, 2 + pkg/service emit): 128-bit hex `SLINIT_INVOCATION_ID` minted at each `initiateStart`, attached to every event in the lifecycle; `--invocation=UUID` filter, `--list-invocations` dedupe
- [x] **Phase 54** (v2.1.9-v2.1.12): journalctl parity completion — 4 Sprints, 9 more flags. Sprint 1 (v2.1.9, 2): `--force` (safety gate for `--setup-keys`), `--synchronize-on-exit` (no-op alias — slinit's sinks always fsync on Close). Sprint 2 (v2.1.10, 3): `--flush` + `--relinquish-var` + `--smart-relinquish-var` via a UNIX DGRAM control socket (Go's `os/signal` doesn't deliver SIGRTMIN reliably, so the initial signal-based design was replaced). Sprint 3 (v2.1.11, 2): `--namespace` + `--list-namespaces` — `slinit-journald --namespace=NS` auto-suffixes every default path (`.NS` on dir/volatile/pid/admin, `-NS.sock` on events socket); `guardedSink` stamps every incoming event with the namespace. Sprint 4 (v2.1.12, 2): `--image` + `--image-policy` via pkg/dissect (losetup + mount + lsblk shell-out; `strict` policy refuses LUKS/LVM/verity partitions). **Journalctl parity project complete: 65 of 65 flags.**

Post-v2.1.12 the phase-numbering system was retired in favour of the
three-lane structure the CHANGELOG carries (new features / security
features / code fixing). Per-version detail from v2.2.0 onward
lives in [CHANGELOG.md](../CHANGELOG.md). Highlights since v2.1.12:

- **v2.2.0–v2.2.4**: full `slinit-journalctl` systemd short-alias
  parity + first-class nspawn-style container integration + state-
  machine race fixes surfaced by fuzz.
- **v2.2.5**: journal multi-boot — `--list-boots` walks on-disk
  journals, `-b -N` relative indexing via aggregator + `demo/run.sh
  --persist` for multi-boot demos.
- **v2.2.6**: journal recovery hardening (six truncation-tolerance
  fixes to pkg/journalbin), enable/disable workflow polish, first
  published cold-boot performance harnesses under
  tests/performance/demo/.
- **v2.2.7**: critical `pkg/config` DirLoader concurrent-map fix
  (PID-1 panic under stress), optional `net/http/pprof` endpoint
  behind `-tags pprof`, SSH performance suite expanded to 92 cases,
  slpkgs `post_install` now creates `/usr/bin/{halt,reboot,poweroff,
  shutdown}` symlinks (fresh installs get working `reboot` out of
  the box), three upstream dinit state-machine consistency fixes
  ported. Validated on both KVM (ceres) and bare-metal (Intel NUC
  Gen7): 219/219 acceptance pass on both.
- **v2.2.8**: docs + upstream-parity pass. 9 new man pages close
  the binary→doc gap (`slinit-supports`, `slinit-{journalctl,
  journald,journal-migrate}`, `slinit-machinectl`, `slinit-nspawn`,
  `slinit-{openrc,runit,systemd}-convert`); 3 dinit state-machine
  consistency ports (`queueForConsole` double-enqueue guard,
  `startCheckDependencies` waiting_for_deps consistency,
  `ExecuteTransition` waitingForDeps-before-queueForConsole
  reorder); docs currency across 50+ Markdown files. No binary
  or config behaviour change.
- **v2.2.9**: finit-parity release — fresh look at finit 5.0-rc1
  as a seventh upstream surfaced 9 actionable items, 8 shipped.
  Highlights: `slinitctl switch-root NEWROOT [INIT]` (initramfs →
  real-root, unlocks LUKS/LVM/NBD/iSCSI boot paths), `pkg/hooks`
  (system-up / system-down / switch-root operator scripts) with
  zero-config `/etc/rc.local` + Debian/BusyBox
  `/etc/network/interfaces` integration + SIOCSIFFLAGS loopback
  bring-up, `slinit.cond=` / `slinit.reboot-watchdog` /
  `slinit.reboot-delay=` kernel-cmdline selectors, `slinitctl
  suspend` / `edit` subcommands, `tty-path = @console` sentinel,
  fuzz-found `pkg/journalbin` DATA-header bounds fix (Xeon
  40-thread hit a `makeslice` panic on a hostile length),
  boot-console UX rewrite so operator-hook / rc.local / ifup
  output no longer races bash's login prompt on `/dev/console`.
  (Skipped: `conflicts:` directive — no user demand, real
  implementation costs 3-5× the audit's state-machine
  integration estimate.)
- **v2.2.10**: finit-parity finalisation + boot-console UX polish
  + one pre-existing regression closed. The Finit 5.0-rc1 feature
  comparison now stands at **22 of 23 items shipped** — the
  remaining two (D-Bus `org.finit` API and dlopen plugin ABI)
  are deliberate-defer notes (README's finit bullet spells out
  the reasoning and revisit triggers). New binaries:
  `slinit-getty` (built-in login-prompt, removes the util-linux
  `agetty` dependency on embedded images) and `slinit-watchdogd`
  (runtime WDT petting daemon with SIGPWR handover; complements
  the shutdown-time WDT already shipped via
  `slinit.reboot-watchdog`). Fixes: `log-forward-udp` standalone
  (pre-existing regression from 2026-02-25 where the
  SyslogForwarder only got constructed inside the LogRotator
  branch), `SyslogFacilityCode` empty-string default (RFC 3164
  says LOG_USER=1, not −1), boot-console catch-all mute
  post-boot + `OnShutdownAnnounce` un-mute so operators still
  see the reboot notice on `/dev/console`, `tests/functional/96`
  BusyBox nc UDP sticky-connect (respawn between self-test and
  assertions), `tests/performance/demo` bimodal +1 s cold-boot
  spike (runit-svc `ready-check-interval` 1 s → 100 ms; boot
  distribution tightened from 2780-3790 ms to 2770-2830 ms).
- **v2.3.0**: first minor bump on the 2.x line, closing the 2.2.x
  correctness phase. The codebase had by then been validated
  continuously against real desktop stacks — XFCE 4.20, GNOME 48,
  KDE Plasma 6, Cinnamon 6 — rather than against test harnesses
  alone.
- **v2.3.1–v2.3.4**: `slinit-logind`, the native
  `org.freedesktop.login1` daemon, and the work to make a graphical
  desktop actually come up on it. Every fix in this stretch was
  surfaced by *using* the desktop on ceres — GDM + GNOME 48 on Xorg
  with elogind's daemon stopped — not by testing it. `slinitctl`
  also gained a systemctl-style `status` / `show` family. elogind's
  *package* is still required for `pam_elogind.so`; only its daemon
  is replaced.
- **v2.3.5–v2.3.6**: `slinitctl start` now waits for the outcome and
  exits non-zero on a failed start (`triggered` services need
  `--no-wait`), plus the first slice of a regression suite built from
  the systemd bug list on nosystemd.org. **v2.3.6 is a PID-1 safety
  release**: on v2.3.5 and earlier, Ctrl+Alt+Del pressed during boot
  panicked the kernel, because signals were claimed far too late in
  `main`.
- **v2.3.7–v2.3.8**: containers and observability. slinit as real
  PID 1 under Docker gained its own suite (`tests/container/`, 23
  cases plus a soak loop) and a Kubernetes one on `kind`
  (`tests/k8s/`, 8 cases); between them they found an early-exit
  hang, lost exit logs and a container that reported a requested
  stop as a boot failure. Also: a Prometheus `/metrics` endpoint
  (`--metrics-listen`, hand-written HTTP so `net/http` stays out of
  PID 1), four degrees of shutdown haste (plain / `now` / `--fast` /
  `--superfast`), the `slinitctl reboot|halt|poweroff` shortcuts the
  man page had been promising, and the first written stability
  commitment in [STABILITY.md](../STABILITY.md).
- **v2.3.9**: a soft-reboot release, every fix found by driving the
  demo VM through repeated soft reboots and looking at what came back
  wrong. The console file descriptors handed to the next generation
  (a soft-rebooted slinit had been concluding it was not on a
  terminal, silently losing the boot console's colour); the kernel
  boot time, which had been reported as the machine's uptime and grew
  with every generation; `slice` without `cgroup`, which had never
  worked since the directive was introduced; cgroup directories that
  nobody reclaimed; and a hurried soft reboot that killed in-flight
  stop-commands, orphaning detached daemons so that every *later*
  boot failed too.

