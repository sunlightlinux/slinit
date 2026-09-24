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

- **Deprecation warnings, the machinery.** STABILITY.md has said since
  v2.3.7 that a deprecated directive "keeps working. Using it produces
  a warning from `slinit-check` and in the daemon log". Nothing did.
  Now the parser consults `features.Deprecation` for every directive it
  applies and calls `config.OnDeprecatedDirective`, which `slinit-check`
  wires to its `WARNING` channel and `slinit` to the daemon log at
  warning level. With no hook wired — every library caller, every test
  — the lookup is not even consulted.

  **Nothing is marked deprecated, so nothing warns yet.** That is the
  point: the rule says a deprecation is announced in a *minor* release,
  and the machinery had to exist before the first one rather than
  arrive with it. Marking one is now a table entry in
  `pkg/features/provenance.go` — `DeprecatedSince` and `ReplacedBy` —
  and a test fails until the CHANGELOG and `slinit-service(5)` say so
  too, so the announcement cannot be forgotten.

  Aliases are explicitly not deprecations, and a test enforces it:
  `termsignal`, `rlimit-addrspace` and `run-in-cgroup` are dinit
  spellings kept deliberately for the life of the major version.

### Fixed

- **`namespace-demo` could not be stopped, only killed.** It runs as
  PID 1 of its own PID namespace, and the kernel does not deliver a
  signal to a namespace's init unless that process installed a handler
  (`pid_namespaces(7)`). With no `trap`, SIGTERM was discarded, so
  every shutdown waited out the 3-second `stop-timeout` and logged
  `stop timeout exceeded, sending killed` for a service that was doing
  exactly what the kernel specifies.

  Adding `trap` is the obvious fix and does not work on its own: a
  POSIX shell runs a trap only after the current foreground command
  returns, so a trapped TERM sits behind `sleep 30` for up to thirty
  seconds. The sleep is now backgrounded and the shell blocks in
  `wait`, which is interruptible. Measured in a real PID namespace:
  no trap and trap-with-plain-sleep both survive SIGTERM; trap with
  `sleep & wait` exits in 0.01s.

  Also on that line: the demo printed a literal `$` where it meant to
  print its PID. slinit expands `$VAR` at parse time and collapses
  `$$` to one `$`, so showing the shell's `$$` needs `$$$$`. It now
  reports `PID inside namespace: 1`, which is the thing the demo
  exists to show.

  `stop-timeout-demo` is deliberately left alone — it carries
  `trap '' TERM` on purpose, and its escalation message is the point.


## [2.4.1] — 2026-09-24

One fix, in `slinit-logind`. **Upgrade if anything on the machine asks
login1 which session a pid belongs to** — a desktop portal, a session
tracker, `loginctl`-style tooling — on a host where processes live
under systemd-style session scopes.

Verified the way CI does it — `go vet` clean, `go test -race -count=1
./...` green across all 72 packages that have tests — and by mutation:
with the check removed, the two cases that stand in for the CI failure
fail again.

The QEMU functional suite last ran at 225/225 against the v2.4.0 tree.
It is not re-run here, and would not have caught this anyway: exactly
one of its 225 cases touches `slinit-logind`, and it exercises
`KillUserProcesses`, not pid-to-session resolution. This bug's natural
habitat is a login session, which the demo VM does not have.

### Fixed

- **`slinit-logind` resolved any pid on a systemd host to a session it
  had never heard of.** `findSessionByCgroup` has two layouts to
  handle: the flat one slinit-logind writes
  (`/sys/fs/cgroup/`*id*, elogind's), and systemd's nested
  `.../session-`*id*`.scope`, kept so a record written by an older
  daemon stays resolvable. The flat branch checked that a session
  record existed before trusting the name; the nested branch returned
  whatever it scraped out of the path. On a host running systemd every
  process sits under a session scope, so `GetSessionByPID` answered
  with an object path for a session that does not exist, instead of
  `NoSessionForPID`. `GetUserByPID` was unaffected — it re-reads the
  record and falls through when it is missing.

  Both layouts now consult the same existence check. The parsing moved
  into `sessionFromCgroup`, which takes the content and an `exists`
  callback, so it can be driven against chosen input rather than
  against whatever cgroup the test process happens to live in — the
  previous test kept its own copy of the parser, which is how a bug in
  the original went unnoticed while the copy stayed correct.

  Found by CI. It could not reproduce from a desktop terminal, whose
  cgroup has no `session-` component, and reproduced on every login
  session and build agent.


## [2.4.0] — 2026-09-24

A documentation release. No change to how slinit runs: the control
protocol, the service-file grammar, the CLI surface and every exit
status are exactly what 2.3.9 shipped. What changed is how much of
that a reader can trust, after a pass over all 66 Markdown files that
checked each claim against the code rather than against the previous
revision of the claim.

Three of the findings were not stale numbers but statements that had
become untrue:

- **The README and `slinit(8)` both said logind session and seat
  management remains "intentionally out of scope".** `slinit-logind`
  has implemented it since v2.3.1 and runs GDM with GNOME 48. Both now
  say what is actually out of scope — systemd's unit object model on
  D-Bus, and the ecosystem daemons.
- **`slinitctl(8)` never mentioned `show`**, while STABILITY.md points
  scripts at `slinitctl show` twice as *the* stable machine-readable
  surface. STABILITY.md's own rule is that anything undocumented is
  not promised, so the two contradicted each other.
- **CLAUDE.md and EXAMPLES.md both cited `activation-timeout`** as an
  existing directive to reason about. It exists nowhere: not in the
  parser, not in the man pages, not in dinit.

### Added

- **`slinit-logind(8)`** — the daemon had shipped in v2.3.1 without a
  man page, and was the last binary without one. Covers the D-Bus
  surface, the `/run` trees it owns and the ones it provides for
  libelogind, why `/run/systemd/system` is deliberately absent, and
  why session activity follows the foreground VT.
- **`slinit-shutdown(8)` gained the seven options it accepted but
  never documented**: `-f`/`--force`, `-n`/`--no-sync`, `-d`/
  `--no-wtmp`, `-w`/`--wtmp-only`, `--no-wall`, `-i`/`--interactive`.
- **`slinitctl(8)` gained `show`** and the `analyze` subcommands
  (`critical-chain`, `dot`, and `plot`, which is a documented
  not-implemented stub).
- `slinit-journald(8)` gained `-version` and `-volatile-dir`;
  `slinit-service(5)` gained `no-boot-marker`, the one directive of
  287 it was missing.
- **STABILITY.md now covers the metrics endpoint and the container
  results file.** Eleven metric names are served at `/metrics` and
  people build alerting rules on them, which makes them an interface:
  names and types are fixed within a major, new ones may appear, and a
  `_total` counter stays monotonic. `/run/slinit/container-results` is
  read by whatever supervises a container, so its file names and their
  meaning are covered too.
- `pkg/features` gained a `finit` source. The README has listed finit
  as an upstream since v2.2.9, but the enum had no value for it, so
  `CmdSwitchRoot` and `CmdSuspend` were filed as slinit-native
  inventions.

### Changed

- **`tools/stats` classifies the feature surface through
  `pkg/features`** instead of its own regex, so it and
  `slinit-supports` cannot disagree about what a directive is. It had
  counted every `case "x":` label in the parser and reported 318; 290
  are directives, and the rest are values an operator can never type —
  service types, scheduling policies, NUMA policies, bare `yes`/`no`.
  Service options and reply codes now get their own lines instead of
  being folded into the directive and opcode totals.
- **`doc/features.md` says how much of itself is curated.** 180 of its
  366 entries are auto-placeholders that fall back to `slinit` as
  their source, so the `slinit` group read as a list of 201 inventions
  when 180 of them are simply un-triaged. The generator now counts its
  own placeholders at render time, which means a regeneration cannot
  drop the caveat — as it had dropped the previous hand-written
  preamble.
- **STABILITY.md admits what it describes but does not have.** Two of
  its commitments — "since X.Y.Z" markers in `slinit-service(5)`, and
  deprecation warnings from `slinit-check` and the daemon log — are
  not built. Nothing is deprecated yet so nothing has been missed, but
  a policy describing machinery nobody wrote is worse than one that
  names the gap. Both now say so, with a section listing them.
- STABILITY.md's History gained v2.3.9's cgroup-directory reclamation.
  Nothing documented promised the directory would outlive the service,
  but an external script writing into a stopped service's cgroup would
  now find it gone, and "a setup could be relying on it" is the test
  this policy applies. It belonged in a minor.

### Fixed

- **`tests/performance/ssh/README.md` printed a command that fails.**
  It told the reader to run `cases/30-ctl-status.sh`; the file is
  `030-ctl-status.sh`, as are all 92 cases. Its section heading had
  also outlived four releases, and the template it hands new
  contributors used a two-digit prefix.
- **`demo/README.md` documented one of `run.sh`'s ten flags.** The
  other nine are boot modes — rescue, emergency, the two debug shells,
  `confirm-spawn`, `panic-after` — each mapping to the kernel-cmdline
  key a real machine would use, which makes them the cheapest way to
  exercise those paths. Now a table.
- **`slinit(8)`'s `SEE ALSO` listed 28 pages and missed 14**,
  including every journal binary, both container tools, all three
  migration converters, and `slinit-logind` itself. `AUTHORS` credited
  three of the seven upstreams.
- **`tests/functional/README.md` now says that a skipped case reports
  PASS.** Roughly one case in ten skips, for want of cgroup v2,
  `util-linux`, `/etc/machine-id`, a TPM or NUMA in the VM. This is
  not housekeeping: `164-slice-hierarchy` asserts exactly the
  behaviour v2.3.9 fixed, and had been skipping since it was written —
  which is how `slice` came to be dropped by the loader for the whole
  life of the directive while keeping a green test.
- Counts corrected wherever they appear: 2111 → 2196 unit tests, 79 →
  81 Go dirs, 302 → 330 `_test.go` files, 29 → 34 packages, 35 → 42
  binaries, 218 → 225 functional cases. "27 fuzz targets" was right
  about `tests/fuzz` and wrong about the repo, which has 40 — the
  other 13 sit beside the code they exercise, so `go test -fuzz=...
  ./tests/fuzz` reaches two thirds of the surface.

### Verification

Checked across all 43 man pages and found clean: cross-references,
cited filesystem paths, every flag of every binary against its page,
and every declared default against the constant behind it.
`slinit-service(5)` was checked in both directions — every directive
the parser accepts is documented, and every directive documented still
exists.

72 unit packages pass. The QEMU functional suite has not been re-run
for this cut; it last ran for v2.3.5. No binary behaviour changed, so
nothing in this release could move it.

## [2.3.9] — 2026-09-23

A soft-reboot release. Every fix below was found by driving the demo VM
through repeated soft reboots and looking at what came back wrong — a
missing blank line that turned out to be a lost terminal, a kernel boot
time that grew with each generation, a `slice` directive that had never
worked on its own, cgroup directories nobody reclaimed, and a hurried
reboot that could break every boot after it.

This is a patch. Everything here is a bug fix or an additive protocol
extension, which is what [STABILITY.md](STABILITY.md) allows in one.
Two of the fixes are visible from outside, though, so they are listed
again under `Changed` with what to check.

Verified: 72 unit packages including `-race` on `pkg/service` and
`pkg/control`, plus the demo VM across repeated soft reboots. The
stop-command grace got a controlled comparison: on 2.3.8 and on this
build the immediate kill fires at the same point in the teardown, right
after `persist-journal-mount`, and where 2.3.8 logged three
`stop command failed (killed by signal 9)` and then failed to start
`ssd-demo` and `supervise-demo` on the next boot — and on every boot
after that — this build logs none and brings both back. The QEMU
functional suite has not been re-run for this cut; it has not run since
v2.3.5.

### Fixed

- **A soft-rebooted slinit lost the boot console's colour and its
  teardown separator.** `syscall.Exec` inherits fd 1 and fd 2, and the
  outgoing instance had them pointing at its own catch-all pipe — whose
  reader dies with the process image. The next generation repaired the
  descriptors by reopening `/dev/console`, but only after it had already
  asked `isTerminal(1)` at the top of `main`, so it spent the rest of
  its life believing it was not on a terminal: no ANSI green on
  `[ OK ]`, and no clear-line before the `[STOPPD]` cascade, which is
  why the blank line after `Shutting down slinit (...)` was there in one
  generation and missing in the next. `SoftReboot` now drains the
  catch-all and puts fd 1/2 back on the console before the exec, and the
  repair path re-samples `isTerminal` so a soft reboot performed by an
  older slinit is handled too. Draining before the exec also stops the
  last lines of the outgoing generation from dying in the pipe.
- **`slinitctl boot-time` reported the machine's uptime as the kernel's
  boot time after a soft reboot**, a figure that grew with every
  generation (`20.150s (kernel)`, then `22.180s`, on a machine whose
  kernel took 550ms). The kernel does not restart during a soft reboot,
  so the new generation cannot measure it; the real figure now travels
  in the soft-reboot snapshot, alongside a count of generations.
  `boot-time` gained a line saying which soft reboot this is and how
  far into the machine's uptime it started. A snapshot from a slinit
  that predates the fields says "kernel time not carried across the
  soft reboot" instead of substituting the uptime.

- **`slice` was ignored unless `cgroup` was set too.** The loader
  applied it only inside the branch guarded by a non-empty `cgroup`, so
  a service configured with `slice` alone ran in the daemon's default
  cgroup — no grouping, no per-slice limits, and no diagnostic saying
  so. `slinit-service(5)` documents slice as sufficient on its own
  ("**cgroup** or **slice** must be set"), and `slinitctl run --slice`
  relied on exactly that. An explicit `cgroup` still wins when both are
  present.
- **A service's cgroup directory is now reclaimed when it stops.**
  slinit creates these itself and nothing removed them, so every
  `slinitctl run --slice=NAME` left one behind — the transient units are
  named `run-<rand>`, so the names never repeat and the directories
  accumulate without bound, surviving soft reboots because a soft reboot
  does not touch cgroupfs. Measured on the demo VM: five invocations,
  five directories still present three soft reboots later. The removal
  is an `rmdir`, so a cgroup that still holds processes or child cgroups
  is left untouched, and a service with neither `cgroup` nor `slice` is
  skipped entirely — its effective path is the daemon-wide default that
  every other unconfigured service shares.
- **One hurried soft reboot could break every boot after it.** `shutdown
  <kind> now`, and `--fast`/`--superfast` on a soft reboot, SIGKILLed
  every service PID at once — and a `scripted` service reports its
  *stop-command* as its PID once the start command is gone. So the
  cleanup script was shot mid-flight. For a service whose daemon is
  detached (`slinit-start-stop-daemon`, `slinit-supervise-daemon`) that
  script is the only thing that ever stops the daemon, so the daemon
  survived with its pidfile intact and the next boot's `--start` refused
  to start over it, exiting 1. The failure then repeated on every
  subsequent boot, hurried or not, until a full kernel boot cleared the
  orphan. A stop-command already running now gets one second before the
  SIGKILL; everything else still dies at once. No other service type was
  affected — `ProcessService` reports its main process and
  `BGProcessService` its daemon, and neither puts the stop-command's pid
  in `PID()`.

### Changed

- **A stopped service's cgroup directory disappears.** It used to
  linger, so anything that wrote to `/sys/fs/cgroup/<slice>/<service>/`
  while the service was down found it there. slinit recreates the
  directory and rewrites every setting from the service's configuration
  on the next start, so nothing slinit manages is lost — but an external
  script that pokes at a stopped service's cgroup needs to create the
  directory itself, or run while the service is up.
- **`shutdown <kind> now` takes up to a second longer when a
  stop-command is running.** That second is the fix above; services
  themselves still die at once, and nothing waits on a stop-command that
  has already finished, which is the normal case. To confirm which
  behaviour a binary has: run a hurried shutdown and watch for
  `stop command failed (killed by signal 9)` — present on 2.3.8 and
  earlier, gone here. A script that outlives its second logs
  `stop command still running after 1s, sending SIGKILL` instead.

### Added

- `BootTimeInfo` carries the soft-reboot count and the generation's
  start uptime in an optional trailing block on `RplyBootTime`. Older
  clients stop reading before it; a newer client talking to an older
  daemon finds no tail and reports a plain boot.
- The soft-reboot snapshot gained `kernel_boot_ns` and `soft_reboots`.
  Both are optional, so snapshots round-trip in either direction.

## [2.3.8] — 2026-09-23

A container and observability release: slinit gets a Prometheus
endpoint, `slinitctl shutdown` gets three faster gears, and the Docker
and Kubernetes suites that found most of the fixes below are in the
tree.

**This is a patch number carrying two behaviour changes that
[STABILITY.md](STABILITY.md) would have put in a minor** — the
container exit code for a requested stop, and what `shutdown <kind> now`
does. Both are in `Changed` below, with what to do about them. Read that
section before upgrading if you script either. The deviation is recorded
in STABILITY.md's History, next to v2.3.5's.

Verified: 72 unit packages, 23/23 container cases, 8/8 Kubernetes cases
on kind. The QEMU functional suite has not been re-run for this cut.

### Changed

- **`slinitctl shutdown` now has four degrees of haste.** Typing `now`
  used to be the same as leaving the time out, because `now` was
  already the default. It now means what it sounds like:

  | Command | What it does |
  |---------|--------------|
  | `slinitctl shutdown halt` | unchanged: SIGTERM, each service's `stop-timeout`, then SIGKILL; sync, unmount, syscall |
  | `slinitctl shutdown halt now` | SIGKILL the services at once — a long `stop-timeout` can no longer hold the machine up; still syncs and unmounts |
  | `slinitctl shutdown halt --fast` | no teardown at all: sync and the syscall, as `reboot -f` does |
  | `slinitctl shutdown halt --superfast` | the syscall alone — no sync, no utmp record, as `reboot -ff` does. Unwritten data is lost and the next boot finds an unclean filesystem |

  Measured against a service that ignores SIGTERM and asks for a 10s
  stop-timeout: 10.3s for the plain form, 0.3s for the hurried ones.
  Neither `--fast` nor `--superfast` can be scheduled, the two refuse to
  be combined, and in container mode, where there is no syscall to make,
  both behave as `now`.

  On the wire this is an optional flags byte after `CmdShutdown`'s type
  byte. A daemon that does not know it reads the type and ignores the
  rest, which is the graceful path — so a new `slinitctl` against an
  older daemon degrades to today's behaviour rather than failing.

- **A requested stop ends a container with 0, not 1.** `slinitctl stop`
  on a container's workload left nothing running, and slinit called that
  a boot failure: exit 1 and `ERROR: Boot failure detected`, for
  something the operator had just asked for. In Kubernetes that is a
  `Failed` pod after a deliberate `kubectl exec … slinitctl stop`.
  Container mode now separates the two — nothing running because a stop
  was requested exits 0 with `All services stopped on request`, while
  nothing running with nobody asking is still a boot failure with exit 1.
  A workload that ran to completion still reports its own code either
  way. `slinitctl shutdown halt now` remains the direct way to end a
  container.

### Fixed

- **`slinitctl shutdown softreboot --fast` halted the machine.** Both
  `--fast` and `--superfast` go to `reboot(2)`, and the type-to-command
  mapping quietly fell through to `LINUX_REBOOT_CMD_HALT` for anything
  it did not recognise. A soft-reboot is not a kernel operation — it
  re-executes slinit in place — so asking for one in a hurry printed
  `reboot: System halted` and the box was gone. `remain` had the same
  hole.

  The haste flags describe work skipped on the way to the kernel, so
  they now only choose a syscall path when the kernel is where the
  request ends. A soft-reboot, `remain`, container mode and anything
  where slinit is not PID 1 all take the kill path instead, hurrying
  the teardown — which for a soft-reboot is the whole of it.
  `slinitctl shutdown softreboot now` was always correct and is
  unchanged. `shutdown.KernelShutdownCmd` now reports whether a type is
  a kernel operation at all, and the force paths say so in the log
  before falling back to a halt.

- **`slinitctl reboot`, `halt` and `poweroff` exist now.**
  `slinitctl.8` has described them as top-level shortcuts for a while —
  "provided so `slinitctl reboot` works as muscle-memory from other init
  systems" — but nothing implemented them: the dispatcher answered
  `Unknown command: reboot`. They now route to the same code the long
  form uses, so they take the same time argument and the same flags:
  `slinitctl reboot now`, `slinitctl halt --fast`, `slinitctl poweroff
  +5`, and `kexec` / `softreboot` too.

- **A container's log is clean now.** slinit no longer warns about a
  missing `/etc/machine-id` in container mode: images routinely ship
  without the file, the identity belongs to the runtime, and there is no
  reboot for it to stay stable across, so the advice ("run
  slinit-init-maker") was not actionable — and it was the first line of
  every `docker logs`. Mount a machine-id in if the journal's host
  identity has to persist. The warning is unchanged for a normal boot,
  where it means something. `journal.SetTransientIDWarning` controls it.
  The clear-line escape slinit writes when shutdown begins is also
  skipped when the console is not a terminal.

- **Three things wrong with what a container's log said.** Reading a
  failing pod's output showed all of them at once:
  - **ANSI colour was emitted even when the output is not a terminal.**
    Colour depended on `NO_COLOR` alone. A container's stdout is the
    runtime's log stream, so the escape sequences were noise, and
    viewers that strip them partially rendered `[ OK ]` as `[ OK []`.
    Colour is now decided by whether the output is a terminal, checked
    before the catch-all logger redirects stdout (asking later always
    answers "no").
  - **A service that died printed `[ OK ]`.** Outside shutdown, a stop
    event rendered with the success marker, so a crash produced an ERROR
    line about the exit code followed by `[ OK ] name`. Stops now render
    as `[STOPPD] name` whether or not the system is shutting down.
  - **The exit reason contradicted itself.** The event loop called every
    "all services stopped" a boot failure, which the container path then
    followed with "workload finished". In container mode the loop now
    reports the fact and lets the container path say what it meant.

- **A workload that succeeded made the container exit 1.** With the boot
  service as the workload, slinit took its exit code — unless that code
  was 0, in which case "all services stopped without a shutdown" was
  read as a boot failure and reported as 1. A Kubernetes Job that did
  its work landed in `Failed`, and `docker run` said the job had failed.
  slinit now distinguishes a workload that ran and finished from
  services that stopped without ever producing a result: the first hands
  over its code, 0 included, and only the second is a boot failure.
  Found by the new Kubernetes suite; the Docker suite only tested a
  non-zero code.

- **`docker logs` went silent once boot finished.** slinit mutes the
  catch-all's console tee when the boot target comes up: on real
  hardware that console is a serial line it shares with getty, and
  `/run/slinit/catch-all.log` keeps the full record. A container has
  neither — the console is the runtime's log stream, and the file dies
  with the container — so everything after boot went nowhere an
  operator could reach. A service crash-looping to its restart limit
  produced no output at all, and neither did the boot failure that
  ended the container. The tee is no longer muted in container mode.

### Added

- **A Prometheus endpoint: `slinit --metrics-listen host:port`** (or
  `unix:/path`), serving `/metrics`. Off unless asked for.

  It exposes what slinit already counts, and nothing else: kernel and
  userspace boot time, whether the boot target is up, services by state,
  and per service whether it is up, whether its last start failed, how
  long it took to start, and how many times the supervisor has restarted
  it. Per-service series are the point — they are what says *which*
  service is flapping. Restart and watchdog counts are counters that
  only go up; the rest are gauges read at scrape time, so an endpoint
  nobody scrapes costs nothing.

  The HTTP is written out by hand rather than with `net/http`: this runs
  in PID 1, and a scrape needs a request line, a status line and a body.
  The endpoint costs the daemon 57 KB, against the megabytes `net/http`
  would have added. There is no keep-alive, no second route, and a
  deadline so a client that connects and says nothing cannot hold a
  connection.

  A unix socket is the safer choice where the scraper is local: the
  metrics name every service and say when each restarted. The socket is
  created 0666 for a non-root scraper, and replaces one left behind by a
  crash.

  Covered by container case 23 (scraped from the host, including a
  service killed from outside and its counter moving) and k8s case k08
  (the kubelet's own httpGet probe against `/metrics`, plus a scrape
  from a second pod through a Service, with the `prometheus.io/*`
  annotations).

- **Ten more container cases (11-20).** Shutdown requested from inside
  with each type (`halt`, `poweroff`, `reboot`, `softreboot`), restart
  after an external SIGKILL, the container ending when nothing keeps the
  boot target up, post-boot log visibility, slinit as PID 2 under
  `docker run --init`, `docker pause`/`unpause`, a service hitting
  `--memory` without taking the container with it, PID 1 surviving
  `--pids-limit` exhaustion, process-tree cleanup on stop, and SIGHUP
  as a no-op.

- **`tests/k8s/`: slinit as a Kubernetes workload.** Six cases on a
  local [kind](https://kind.sigs.k8s.io/) cluster, where a kubelet
  rather than a runtime CLI is in the middle: pod lifecycle and
  `kubectl exec`, a batch workload's exit code deciding Succeeded vs
  Failed, `kubectl logs` carrying the reason a pod died, `restartPolicy:
  Always` restarts, readiness and liveness probes driven by `slinitctl`,
  and a SIGTERM-ignoring service killed by slinit rather than by the
  kubelet at the end of the grace period. Service files arrive as a
  ConfigMap. The cluster is created once and reused; its kubeconfig
  stays in `tests/container/_build`, leaving `~/.kube/config` alone.

## [2.3.7] — 2026-09-22

A container-mode release, and the first to ship a written stability
commitment ([STABILITY.md](STABILITY.md)). **Upgrade if you run slinit in
containers:** a workload that exits immediately left `slinit -o` running
forever, and a container that failed to start gave no reason in
`docker logs`. No wire protocol change, no config surface change, no
upgrade action.

Verified with the new `tests/container/` suite on Docker 29.7: 10/10
cases, and a 200-cycle spawn/shutdown soak with no failures and no
runtime SIGKILLs (ready p50 403ms / max 591ms, stop p50 1219ms / max
1290ms, stop time dominated by a deliberate 1s stop-timeout).

### Fixed

- **A boot service that exited instantly left slinit waiting forever.**
  The boot cascade runs before the event loop, and the service set only
  created its "a service went inactive" channel when the loop first
  asked for it. A service that died in that gap had its notification
  dropped, so the loop never noticed that nothing was running. In a
  container, `slinit -o` whose workload exited at once never exited
  itself. As bare-metal PID 1, the same boot hung without reaching the
  recovery menu. The channel now exists from the start, and the
  notification waits for the loop.
- **The last log lines before an exit were lost.** `os.Exit` skips the
  deferred catch-all `Stop()`, so whatever was still in the catch-all
  pipe never reached the console. Those are the lines that say why
  slinit is exiting: a container with a missing service exited 1 with
  nothing in `docker logs` but startup warnings. Every exit from `main`
  now drains the catch-all first, with a 2s cap in case an orphan still
  holds the pipe open.
- **Two warnings on every container start.** slinit re-set the hostname
  from `/etc/hostname`, although the runtime had already set it to the
  same value, and the call failed without `CAP_SYS_ADMIN`. It now does
  so only when the name differs. The interactive boot debugger no longer
  starts in container mode. It failed to open a `/dev/console` that does
  not exist without a tty, and its reboot and poweroff actions take the
  bare-metal shutdown path, which bypasses the container exit code.
- **`slinit-logind`: the GDM greeter stayed blank after a quick
  logout.** Every session reported `Active=yes` forever, and
  gnome-shell's greeter fades its login dialog back in only when its
  session's `Active` changes to true. Session activity now follows the
  foreground VT, the way elogind decides it. `Seat.ActiveSession` and
  `Seat.Sessions` are updated as sessions change, where they used to be
  fixed at startup. `ActivateSession*` and `Seat.SwitchTo` really switch
  VTs. Still open: after a logout shorter than gdm-x-session's 10s
  registration delay, the re-used greeter shows but its password field
  stays inactive; `slinitctl restart gdm` recovers.

### Added

- **`tests/container/`: slinit as PID 1 of a real container.** Ten
  host-driven cases pin container mode's contract under Docker: exit
  code propagation (including 128+signal), fast failure on a broken
  service tree, zombie reaping, SIGINT and SIGRTMIN+3/+4/+5 halt codes,
  stop-timeout and SIGTERM escalation, `docker exec slinitctl`, and a
  read-only rootfs. `soak.sh` repeats the spawn/shutdown cycle (100 by
  default) and reports non-zero exits, runtime SIGKILLs and slow stops.
  Before this suite, container mode was only exercised with slinit as a
  child of a shell. Its first run found the first three fixes above.
  Podman is accepted but has not been run.

- **`STABILITY.md` — a written stability commitment.** It says which
  surfaces are stable and for how long: `CPVersion=7` until v4.0.0,
  service directives not removed or reinterpreted within a major, and
  `slinitctl` exit statuses. It also sets the semver rules the releases
  follow and the deprecation path, and says what is not covered: the Go
  packages under `pkg/` and human-readable output. Its History section
  records two earlier deviations. v2.3.5 changed `slinitctl start` in a
  patch release. The v2.3.0 entry below says an older parser silently
  skips an unknown directive, but it has always been a load error.

### Compat

- Wire protocol, config surface, package manifests: unchanged.
- Runtime deps: unchanged.
- **The boot debugger (Ctrl-B at boot) no longer runs in container
  mode**, including under `docker run -t`. Under STABILITY.md's rules
  this is a fix, not a removal: without a tty it could not start, and
  with one its reboot and poweroff actions took the bare-metal shutdown
  path, so the container's exit code was never written.
- **`slinit-logind` sessions on a background VT now report
  `Active=no`, `State=online`**, and `ACTIVE_SESSIONS` in
  `/run/systemd/users/*` lists only foreground sessions. elogind has
  always behaved this way. Sessions without a seat (ssh) and seat0
  sessions without a VT number stay active, as before.

## [2.3.6] — 2026-09-21

A PID-1 safety release. **Upgrade if you run slinit as init on a machine
with a keyboard:** on v2.3.5 and earlier, Ctrl+Alt+Del pressed during
boot panics the kernel. No wire protocol change, no config surface
change, no upgrade action.

### Fixed

- **Ctrl+Alt+Del during boot killed PID 1, and the kernel panicked.**
  Observed as `Kernel panic - not syncing: Attempted to kill init!
  exitcode=0x00000200` on a rescue-mode boot.

  `InitPID1` turns off the kernel's own Ctrl+Alt+Del handling, so the
  key arrives as SIGINT to PID 1. The kernel only drops a signal to
  PID 1 when PID 1 has no handler for it. The Go runtime installs
  handlers for every signal at startup, so that protection never
  applies to slinit. Until `signal.Notify` claims a signal, the
  runtime's default governs it, and for SIGINT that default is
  `exit(2)`. slinit only claimed its signals in `EventLoop.Run`,
  roughly 1600 lines into `main`. Mounts, service loading and the whole
  start cascade ran before that point with nothing claimed. The same
  applied to SIGTERM, SIGQUIT, SIGHUP, SIGUSR1, SIGUSR2 and the
  systemd-compatible RT shutdown signals.

  The shutdown signals are now claimed as the first thing `main` does,
  and the event loop adopts that channel. SIGCHLD is deliberately still
  claimed later, by `Run`. Nothing reads it during boot, orphan reaping
  would fill the channel's buffer, and once the buffer is full the Go
  runtime drops further signals, including the Ctrl+Alt+Del this fix
  exists to catch.

- **Ctrl+Alt+Del did nothing at a recovery prompt.** With the panic
  fixed, the key was caught but never acted on while slinit waited at
  the boot-failure menu (`recovery.Present`, before the loop starts) or
  the collapse menu (`recovery.PresentCollapse`, after it returns). In
  both places the event loop is not running. The only way out was the
  60s auto-reboot. Both prompts now reboot on a shutdown signal. A
  single press reboots in about 2s, confirmed on real hardware.

- **Rescue and emergency mode read their own empty service set as a
  collapse.** `checkInactive` saw no running services, which is the
  whole point of rescue mode, and ended the event loop the moment the
  mode started. That closed the signal channel, which is the other
  reason Ctrl+Alt+Del did nothing there.

### Security

- **`_COMM`, `_EXE` and `_CMDLINE` in the journal could be forged.**
  The receiver was meant to trust only its own `/proc` snapshot of the
  sender, never the sender's word. But it overwrote those fields only
  when the snapshot succeeded. A sender that exited straight after
  logging kept whatever values it had claimed, so any local user could
  stamp log lines with any command name. The fields are now cleared
  before the snapshot: an entry from a vanished sender has no command
  attribution, rather than a forged one.

### Added

- **`tests/functional/cad-recovery-test.sh`**, a host-driven test. It
  boots a VM whose boot service cannot load, waits for the recovery
  menu, and presses the real key combination through QEMU's monitor
  (`sendkey ctrl-alt-delete`), not a `kill(2)` imitating it. Pass means
  the guest rebooted. Against a pre-fix build it reproduces the
  reported panic exactly, down to `exitcode=0x00000200`. `run-tests.sh`
  does not run it; run it explicitly.

- **Guards against the signal fix regressing.** Functional case 225
  checks that PID 1 keeps running after the signals it should survive
  (USR1, HUP, PIPE, KILL, STOP). `TestShutdownSignalSet` pins the
  signals it must claim instead: a signal dropped from that set gets
  Go's fatal default back. The unit test is needed because a guest VM
  cannot tell its own orderly reboot from its own panic.

- **The rest of the nosystemd.org checklist:** `#2913` (a message from
  a process that has already exited keeps its attribution, which is
  how the Security item above was found), `#6237` (an account named
  `0day` resolves by name, not as UID 0), and system-wide resource
  limits (a malformed rlimit is a load error, not silently ignored).

### Corrected

- **`#2460` is not a slinit bug.** v2.3.5 listed it as open and implied
  `slinitctl status` gets slower as the disk journal grows. It does
  not. `status` asks PID 1 over the control socket and is answered from
  the in-memory ring of at most 4096 events, whatever the size of the
  journal on disk. The linear scan only runs when you read a journal
  file explicitly, with `--file`, `--directory` or `-b` for a past
  boot. The test now bounds that path, and its comment says so. Of the
  checklist, only `#11810` (suspending twice) and `#72759` (ecryptfs
  unmount on logout) remain. Both need hardware or setup the test VM
  does not have.

### Compat

- Wire protocol, config surface, package manifests: unchanged.
- Runtime deps: unchanged.
- Journal entries from a sender that exited before its `/proc`
  snapshot now have empty `_COMM`/`_EXE`/`_CMDLINE`, where they
  previously kept the sender's claimed values.

## [2.3.5] — 2026-09-20

A correctness release for `slinitctl start`, plus the first slice of a
regression suite built from the systemd bug list on nosystemd.org. One
commit's worth of work; no wire protocol change, no config surface
change.

**Read the Changed section before upgrading if you script slinitctl** —
`start` now blocks until it knows the answer.

### Changed

- **`slinitctl start` now waits for the outcome and its exit code
  reflects it.** This is a behaviour change for every caller.

  It previously returned as soon as the daemon accepted the request:
  `handleStartService` writes the ACK straight after queueing the
  start, so `slinitctl start foo && echo ok` printed `ok` for a service
  that had just failed to launch. `dinitctl` has always waited and
  returned 1 on `FAILEDSTART`, so this was a parity gap as well as the
  shape of systemd#6478 ("systemctl should not consider active->failed
  as a successful operation").

  `start` now blocks until the service reaches a terminal state and
  exits non-zero on failed-start, start-cancelled, or stopped. The
  events were already on the wire — `allocHandle` auto-subscribes and
  `readReply` was discarding them; it now stashes them, because the
  daemon emits from the service state-machine goroutines and a fast
  service can report Started *before* the command reply arrives.

  **What to change in your scripts:** pass `--no-wait` where you
  relied on immediate return. The one case where this is not optional
  is a `triggered` service: it sits in STARTING by design until
  `slinitctl trigger` fires, so `slinitctl start` on one blocks until
  something else triggers it. The in-tree demo hit exactly this and
  deadlocked its own boot; `demo/services/trigger-autofire` now uses
  `slinitctl --no-wait start`.

  Performance figures move with it: the `CtlStart_*` cases in
  `tests/performance/ssh/` used to measure an IPC round-trip and now
  include service startup, which is what that tier's README always
  claimed to be measuring.

  `slinitctl run` is deliberately exempt. It has its own settling
  logic gated on `--wait` / `--collect`, so it starts the transient
  unit with `--no-wait`; otherwise a plain `slinitctl run` of a
  scripted unit would block until the command finished, which is the
  opposite of what that subcommand is for.

### Added

- **Regression suite derived from the systemd bug list on
  nosystemd.org.** Each case names the upstream issue it mirrors and
  asserts slinit does not have the equivalent defect — the value is
  less the regression cover than that each test documents a design
  decision by contrast.

  Unit: `#4863` (a NUL in a log line must not truncate the record),
  `CVE-2018-16864` (oversized lines stay bounded), `#5644`
  (`R! /dir/.*` must not escape the directory it was given — slinit
  does no glob expansion, and the test exists so adding it later
  cannot reintroduce the bug), `#1596` (`-n` selects the newest
  entries, `-r` only reorders them), `#6369` (a trailing-dot FQDN is
  refused cleanly, before anything is written).

  Functional: `#1143` (a wall-clock step must not wedge or spin PID 1),
  `#1312` + `#6478` (a failed hard dependency leaves the dependent down,
  without a restart loop, and the CLI reports it), Debian `#825394`
  (a detached background job survives session teardown), `#2402`
  (efivarfs is never mounted read-write), `#6620` (the log sink can
  turn over completely beneath a running producer without silencing
  it — slinit has no separate journal process whose restart could
  orphan a writer).

  Six items from the list remain: `#2913` (attributing messages from
  a process that has already exited), `#6237` (user names starting
  with a digit), system-wide resource limits, `#2460` (`status`
  latency with a disk journal), `#11810` (suspending twice) and
  `#72759` (ecryptfs unmount on logout). The last two need real
  suspend hardware and an ecryptfs setup.

### Compat

- Wire protocol, config surface, package manifests: unchanged.
- Runtime deps: unchanged.
- **Scripts calling `slinitctl start` may need `--no-wait`** — see
  Changed. This is the only upgrade action.

## [2.3.4] — 2026-09-20

One commit, two `slinit-logind` fixes, both surfaced within hours of
v2.3.3's ISO landing on ceres — by using the GNOME desktop rather than
by testing it. No wire protocol change; no config surface change.

**Upgrade if you run a graphical session**: without this, locking the
screen from the GNOME menu is a one-way trip.

### Fixed

- **A locked GNOME screen could never be unlocked.** The password was
  accepted every time — GDM logged `pid 1491 reauthenticated user 1001
  with service 'gdm-password'` — and the shield stayed up.

  gnome-shell lifts the lock screen from exactly one place
  (`js/ui/screenShield.js`):

  ```js
  this._loginSession.connectSignal('Unlock', () => this.deactivate(false));
  ```

  There is no verification-complete handler that does it. `/usr/bin/gdm`
  carries the string `UnlockSession` next to
  `org.freedesktop.login1.Manager`, so the chain is: shell asks GDM to
  reauthenticate → GDM calls `Manager.UnlockSession` → logind emits
  `Unlock` on the session object → the shell deactivates.

  `UnlockSession` was `return checkSess(id)`: it confirmed the session
  existed, answered success, and emitted nothing. GDM believed it had
  done its job and the shell was never told. `LockSession`,
  `UnlockSession` and the plural forms now emit, and the signals are
  declared in the session's introspection.

- **`Session.SetLockedHint` discarded the value**, so
  `loginctl show-session -p LockedHint` answered `no` with the screen
  plainly locked. It is stored now, and the write emits
  `PropertiesChanged`. Keeping it needs the `prop.Properties` handle
  that `prop.Export` returns and `registerSessionObject` used to drop on
  the floor; it is retained per session, which is what any future
  property update will need too.

- **`GetSessionByPID` failed for every process except a session's own
  leader.** `findSessionByCgroup` scanned for systemd's nested
  `session-<id>.scope` naming, which slinit-logind never writes — we
  place sessions flat at `/sys/fs/cgroup/<id>`, elogind's layout, where
  the session id is the FIRST path component. That is precisely what
  libelogind's `cg_path_get_session()` reads, and the whole reason the
  layout is flat.

  So the fallback only ever matched the one pid
  `findSessionByLeaderLocked` already covers, and missed everything
  forked from it: `GetSessionByPID` on a live desktop's gnome-shell
  answered `NoSessionForPID` while `/proc/<pid>/cgroup` read `0::/c2`.
  It also broke `/org/freedesktop/login1/session/self` for non-leaders;
  `.../auto` only worked because it falls back to the user's display
  session. The nested form is still accepted after the flat one, so a
  host that really does use systemd's layout stays resolvable.

### Known issues

Unchanged from 2.3.3: GDM's Wayland greeter still paints nothing
(`WaylandEnable=false` is the workaround), and `PauseDevice` /
`ResumeDevice` are declared but not emitted, so a VT switch away from
and back to a Wayland session is not expected to hand DRM master over
correctly.

### Compat

- Wire protocol, config surface, cmdline, package manifests: unchanged.
- Runtime deps: unchanged.
- elogind is still required — see 2.3.3.

## [2.3.3] — 2026-09-20

Point release on the 2.3.x line. Four `slinit-logind` commits plus one
log-rotation fix, all driven by bringing GDM + GNOME 48 up on ceres
against slinit-logind with elogind's daemon stopped. Five commits since
v2.3.2; no wire protocol change; no config surface removal.

The headline outcome: **GDM's greeter, a full GNOME login and a running
GNOME desktop all work under slinit-logind on Xorg.** The Wayland
greeter still comes up blank — that is called out under *Known issues*
rather than glossed, and it is not fixed here.

### Highlights

- **`org.freedesktop.login1.Manager` now has a property dict**
  (`cmd/slinit-logind/managerprops.go`, `802295b`). All 52 of
  elogind's Manager properties are served; previously the path
  answered method calls but carried no properties at all, so
  `loginctl show` came back empty and anything reading lid, AC or
  inhibitor state off the Manager silently used its own defaults.

  Where elogind reports what its `logind.conf` *would* do, we report
  what slinit-logind *actually* does: every `Handle*Key`,
  `HandleLidSwitch*` and `IdleAction` is `ignore`, because there is no
  evdev button watcher and no idle timer. That is also the better
  answer for the desktop — gnome-settings-daemon and
  xfce4-power-manager read these to decide whether logind already owns
  the key, and `ignore` tells them to handle it themselves. Same
  reasoning for `RemoveIPC=false` and `InhibitDelayMaxUSec=0`.
  `LidClosed`, `OnExternalPower`, `NCurrentSessions`,
  `NCurrentInhibitors`, `SleepOperation` and `RuntimeDirectorySize`
  are genuinely derived from procfs, sysfs and `/sys/power/state`.

- **`/run/systemd/users/<uid>` and `/run/systemd/seats/<id>` are
  rebuilt from every live session** (`cmd/slinit-logind/session.go`,
  `3b9381b`). This is what unblocked GDM.

  `sd_uid_get_sessions()` — the lookup mutter falls back to when
  `sd_pid_get_session()` comes up empty — reads nothing but the
  `SESSIONS` / `ACTIVE_SESSIONS` / `ONLINE_SESSIONS` arrays out of the
  user file, and we wrote only `NAME`, `STATE` and `RUNTIME` there. The
  fallback enumerated zero sessions and gnome-shell exited before
  painting. The seat record had the mirror-image bug: it was rendered
  from the single session being created, which clobbered the
  `IS_SEAT0` / `CAN_MULTI_SESSION` / `CAN_TTY` / `CAN_GRAPHICAL` flags
  written at startup, so `sd_seat_can_graphical()` began answering "no"
  the moment the first session appeared. Both are aggregates over every
  live session, so neither can be derived from the one at hand.

- **Sessions are reaped when their leader dies**
  (`cmd/slinit-logind/session.go`, `ccb9fc9`). `pam_close_session`
  calls `ReleaseSession` on a clean logout, but a crashed greeter or a
  SIGKILLed session never got there and the record stayed registered
  forever. The consequence is not subtle: gdm, seeing a greeter session
  still listed on seat0, refuses to start a replacement, so one crashed
  greeter wedges the display manager until the files under `/run` are
  deleted by hand. A goroutine now parks in `poll()` on the leader's
  pidfd — no timer, no cost while the session lives — and startup reaps
  records whose leader is already gone, since the state in `/run`
  outlives the daemon.

- **`Session.TakeControl` / `TakeDevice` are real**
  (`cmd/slinit-logind/device.go`, `ccb9fc9`). DRM master is claimed
  explicitly on `TakeDevice`, with elogind's EBUSY retry, instead of
  relying on the kernel's implicit grant to the first opener. Devices
  are tracked per session, so `ReleaseDevice` closes the one it names
  and `ReleaseControl` closes the rest — `TakeDevice` used to hand out
  a descriptor and deliberately leak ours, which left a
  master-holding DRM fd nobody could close.

- **The `self` and `auto` session object paths are served**
  (`cmd/slinit-logind/alias.go`, `7b757ad`).
  `/org/freedesktop/login1/session/self` is the caller's own session;
  `.../auto` is that, falling back to the display session of the
  caller's user. gnome-shell's greeter runs without `XDG_SESSION_ID` in
  its environment and builds a proxy on `.../session/auto` to find
  itself; with no object there the proxy still constructed, `Id` came
  back undefined, and the following `GetSession(null)` failed with
  "Argument string may not be null".

### Added

- Manager properties: all 52 from elogind's vtable, name- and
  signature-compatible. `TestManagerPropCoverage` pins the set against
  that vtable and `TestManagerPropSignatures` checks each getter
  marshals to the signature it declares.
- `/org/freedesktop/login1/session/{self,auto}` object paths, with the
  full Session method and property surface, resolved per caller.
- `Manager.GetSession("self"|"auto")`; `loginctl` passes `auto`
  whenever it is asked about the current session.
- `pid == 0` meaning "the caller" on `Manager.GetSessionByPID` and
  `Manager.GetUserByPID`, matching elogind, which resolves it from the
  bus message's credentials.
- `systemd1.Manager.GetUnitByPIDFD`, which polkitd reaches for before
  `GetUnitByPID` on every authorisation check.
- `--debug` now traces session setup: the leader pid, the resolved
  ancestor chain with comms, each cgroup migration with a read-back of
  where the pid actually landed, and the scope's settled membership.
  Session setup is the one place a post-mortem is useless — the
  gdm/gnome fork chain is gone within a second of a failure.

### Fixed

- `logfile-max-files` was briefly exceeded during rotation
  (`pkg/service/logrotate.go`, `abcbefd`). `rotateLocked()` renamed
  first and pruned second, leaving `maxFiles + 1` rotated files on disk
  in between; it now prunes to `maxFiles - 1` before the rename, so the
  rename brings the count to exactly `maxFiles`. Functional test 46 was
  intermittently failing on this and now also freezes the producer
  before counting — at `max-size=512` each rotated file lived under a
  millisecond, so `ls` raced the rotator both ways.
- The cgroup migration climbed the ancestor chain unconditionally. On
  gdm the second entry is `/usr/bin/gdm` itself, so the display manager
  was dragged into a session scope it could never be `rmdir`'d out of —
  the source of the litter of empty `/sys/fs/cgroup/c*`. Migration now
  stops at the first pid that verifiably lands in the scope; the trace
  above shows the caller is `gdm-session-worker` and that migrating it
  alone is sufficient.
- The `gdm-session-worker` `/proc` scan is now the fallback it was
  meant to be, running only when the ancestor chain found nothing. Run
  unconditionally it swept up the workers of *other* live sessions and
  moved them into the session being created, which on a machine with a
  greeter plus a logged-in user re-parented the wrong one.

### Changed

- `ensureSeat` renders `/run/systemd/seats/<id>` through the shared
  writer instead of keeping its own copy of the field list. Field sets
  and ordering for both the user and seat records follow elogind's
  `user_save()` and `seat_save()`.
- `sessionPropValues()` is now the single source of truth for the
  Session property dict; `registerSessionObject` builds its `prop.Prop`
  table from it rather than carrying a second copy, so the per-session
  objects and the new alias paths cannot drift apart.
- Sessions are reported active across the board in the aggregate
  records. Without VT-activity tracking there is no basis to call one
  session foreground and another not, and the per-session records
  already said `ACTIVE=1`.

### Known issues

- **GDM's Wayland greeter comes up blank.** mutter takes
  `/dev/dri/card0` through `TakeDevice`, initialises KMS, and scans out
  a framebuffer it allocated itself — but the shell's UI never appears
  in it. The Xorg greeter on the same gnome-shell, in the same session
  mode, renders and logs in normally, so this is not session lookup:
  `.../session/auto` resolves on both. Workaround is
  `WaylandEnable=false` in `/etc/gdm/custom.conf`. Root cause not yet
  identified.
- `PauseDevice` / `ResumeDevice` are still not emitted, so a VT switch
  away from and back to a Wayland session is not expected to hand DRM
  master over correctly. Untested.
- `dropAndClose` calls `drmDropMaster` on our descriptor, which shares
  an open file description with the client's. A compositor that calls
  `ReleaseDevice` without closing its own copy would have master pulled
  out from under it. Not observed in practice.

### Compat

- Wire protocol: unchanged from 2.3.2.
- Config surface: unchanged.
- Cmdline: unchanged.
- Package manifests: no rename or removal. `slinit-logind` and
  `pkg/service` are the only areas touched.
- Runtime deps: unchanged (still `godbus/dbus/v5` as the only
  non-`x/sys` runtime dep).
- **elogind is still required.** `pam_elogind.so` is what calls
  `CreateSession`; slinit-logind implements only the D-Bus server side.
  On Void the `elogind` package also owns that module, `loginctl` and
  `busctl`, and `gnome-shell` and `xfce4` depend on it. Removing it
  needs a native `pam_slinit.so` first.

## [2.3.2] — 2026-09-19

Point release on the 2.3.x line — three focused corrections to the
`slinit-logind` daemon shipped in 2.3.1, all surfaced by live
integration testing on ceres (XFCE via LightDM and GDM+GNOME 48
via elogind-reference). Three commits since v2.3.1; no wire
protocol change; no config surface removal.

### Highlights

- **`login1.Manager.Reboot` (and PowerOff/Halt) now actually
  reboot the machine** (`cmd/slinit-logind`, `6014144`).
  `runShutdown()` was invoking `slinit-shutdown reboot`
  positional-verb style, but slinit-shutdown parses its argv as
  short/long flags (`-r`/`--reboot`, `-h`, `-p`). The child got
  rejected with "Unrecognized option: reboot" and exited
  non-zero, yet `exec.Command.Start()` only reports fork/exec
  failures so `Manager.Reboot()` still returned success. XFCE's
  Restart button called login1.Reboot on our stub, the D-Bus
  reply said "yes", xfce4-session tore down the session
  expecting a reboot in flight — but the machine stayed up.
  Net effect was a silent logout with no reboot.

  Fix: map the systemd-style verb (`reboot`/`halt`/`poweroff`)
  to the correct slinit-shutdown flag (`-r`/`-h`/`-p`) in one
  small switch before exec. Unknown verb returns
  `org.freedesktop.login1.Error.OperationInProgress` so a future
  caller for something unsupported (e.g. `kexec`) gets a real
  error back instead of a silent no-op. Verified live: `sudo -u
  sunlight busctl call ... Reboot` from an active XFCE session
  triggers `execve("/usr/sbin/slinit-shutdown", ["-r"])` followed
  by SIGTERM from PID 1.

- **systemd1 stub steers `gnome-session-*` StartUnit into
  gnome-session's non-systemd fallback path**
  (`cmd/slinit-logind/systemd1.go`, `c8bdb6b`). The stub was
  returning success + emitting JobRemoved on every StartUnit,
  which convinced gnome-session-binary the target was starting
  but not that it had reached `ActiveState=active` — elogind
  + real systemd emit a rich signal stream (UnitNew,
  PropertiesChanged on the unit path, JobNew, ...) on that
  transition, which our stub can't fake without also faking the
  entire dependency tree those signals refer to. After ~10s of
  silence gnome-session-binary printed "Session termination
  requested" and unwound without ever spawning gnome-shell.

  Fix: return `org.freedesktop.systemd1.LoadFailed` from
  StartUnit when the unit name starts with `gnome-session-`.
  gsm-manager.c handles that error by falling into the
  `Falling back to non-systemd startup procedure due to error:
  %s` code path, which reads gnome-login.session's
  RequiredComponents and execs each .desktop's Exec directly —
  the same end state Chimera Linux forces at compile time with
  `-Dsystemduserunitdir=/tmp`. gnome-session-binary now
  progresses past StartUnit, loads
  `/usr/share/gnome-session/sessions/gnome.session`, resolves
  all 17 RequiredComponents (org.gnome.Shell + 16
  org.gnome.SettingsDaemon.*), and reaches "Done adding required
  components". Also adds a lightweight per-method dispatch
  tracer behind the daemon's existing `--debug` flag —
  `systemd1-stub: <method>(<args>)` per call.

- **`CreateSession` migrates its whole PAM ancestor chain plus
  any gdm-session-worker into the session cgroup**
  (`cmd/slinit-logind/session.go`, `b0c6d2b`). libelogind's
  `sd_pid_get_session()` reads `/proc/<pid>/cgroup` and takes
  the first path component — every process under a session must
  land inside `/sys/fs/cgroup/<sid>` or session lookup fails.
  The prior code wrote only the raw D-Bus caller PID and
  swallowed errors: when the caller was a transient PAM helper
  (pam_elogind's account-helper) that exited between the write
  and the scope's next observed state, the scope ended up
  empty — polkit stopped granting "active local session" and
  gnome-shell died with "Failed to setup: Failed to find any
  matching session".

  Fix does three things: (a) walks `/proc/<pid>/status:PPid`
  synchronously inside CreateSession to snapshot the caller's
  ancestor chain before any process in it can exit, then
  migrates every entry (not just the first — kernel accepts a
  dead-pid write silently, so a chain-wide migration is the
  only way to guarantee the scope isn't left empty); (b) scans
  `/proc` for any process whose comm starts with
  `gdm-session-wor` and migrates that too, without a uid filter
  (Void's PAM helper parent chain runs through `/usr/bin/gdm`
  itself, not through gdm-session-worker; explicitly migrating
  the worker gives the scope a long-lived root the fork tree
  can inherit from); (c) redirects `os.Stderr` to
  `/var/log/slinit-logind.log` when `--debug` is set, because
  slinit's runner attaches services' fd 2 to `/dev/null` and
  the stub's diagnostic prints were silently vanishing. Only
  the redirect path is `--debug`-gated; production (`--debug`
  off) behaviour is unchanged.

  Live-tested on ceres: CreateSession fires with the expected
  chain, both ancestors get written, /proc scan finds
  `gdm-session-wor` and migrates it, gnome-session-binary
  reaches the fallback path and resolves the full
  RequiredComponents list. gnome-shell still surfaces "Failed
  to find any matching session" on the very next stage — on
  Void the gdm-session-worker → user-session fork chain
  appears to detach from the migrated worker in a way this
  scan doesn't fully catch. That gap is where the next
  diagnostic pass lands; the `/var/log/slinit-logind.log`
  redirection from this same commit is the input for it.

### Added

- `slinit-logind --debug` now redirects the daemon's own stderr
  to `/var/log/slinit-logind.log`, so the systemd1 stub's
  dispatch trace and the session-machinery's diagnostics
  survive the slinit runner's default fd 2 → `/dev/null`.
  `--debug` off (production default) is unchanged: stderr keeps
  going to /dev/null the way it did in 2.3.1.
- `readPPid(pid)` helper: parses `PPid:` out of
  `/proc/<pid>/status`. Used by `CreateSession`'s synchronous
  ancestor walk.
- Per-method dispatch tracing in the systemd1 stub. Every call
  through `Set/Unset/UnsetAndSetEnvironment`, `GetUnit`,
  `LoadUnit`, `GetUnitByPID`, `Subscribe`/`Unsubscribe`,
  `Reload`/`Reexecute`, `ResetFailed(Unit)`, `ClearJobs`,
  `KillUnit`, and the shared `startJob` router prints one
  `systemd1-stub: <method>(<args>)` line to the daemon's
  stderr when `--debug` is set. Zero cost when off.

### Fixed

- `Manager.Reboot`/`PowerOff`/`Halt`/`WithFlags` variants now
  invoke `slinit-shutdown` with the correct flag argument
  (`-r`/`-p`/`-h`) instead of a positional systemd-style verb.
  The prior code produced a silent no-op — the child exited
  non-zero and the D-Bus reply reported success anyway. XFCE's
  Restart button hits this path, so the practical outcome was
  "click Restart, session logs out, machine stays up".
- `CreateSession` no longer leaves session cgroups empty when
  the D-Bus caller is a short-lived PAM helper. `sd_pid_get_
  session()` on downstream processes now returns the session
  id instead of nothing, which was the underlying cause of
  polkit's "not an active local session" refusals under
  slinit-logind and gnome-shell's "Failed to find any matching
  session" abort.

### Changed

- systemd1 stub's `StartUnit` now returns
  `org.freedesktop.systemd1.LoadFailed` when the unit name has
  the `gnome-session-` prefix. This is a behavioural change vs
  2.3.1 (the stub used to silently succeed on every StartUnit)
  and steers gnome-session-binary into its non-systemd fallback
  path, which actually spawns gnome-shell via the legacy
  autostart flow. Other StartUnit callers (non-GNOME) still see
  the inert success path from 2.3.1.

### Compat

- Wire protocol: unchanged from 2.3.1.
- Config surface: unchanged.
- Cmdline: unchanged.
- Package manifests: no rename or removal. `slinit-logind` is
  the only binary touched.
- Runtime deps: unchanged (still `godbus/dbus/v5` as the only
  non-`x/sys` runtime dep).

## [2.3.1] — 2026-09-17

Point release on the 2.3.x line. Two focused feature additions
(**slinit-logind**, the native `org.freedesktop.login1` daemon,
and a **systemctl-style status/show** family for `slinitctl`) plus
a small correctness cluster (one write-deadline leak, one PID-1
hostname-seeding fix that had been silently mis-tagging early
journal entries). No wire-protocol change; no removal of any
existing surface.

Development shape: 17 commits since v2.3.0 across three tracks
covered in Highlights below. Full unit + acceptance + functional
suite green on ceres (Void x86_64 KVM, kernel 7.2.3-lowlatency-
sunlight1); slinit-logind additionally validated on a fresh
XFCE + LightDM stack (session `c2` allocated on seat0/VT7, full
xfce4-panel + xfce-polkit + xfsettingsd tree healthy) as a live
elogind replacement.

### Highlights

- **slinit-logind — native `org.freedesktop.login1` daemon**
  (`cmd/slinit-logind/`, ~1.9k LOC across 7 commits). First
  runtime dep beyond `x/sys` (`godbus/dbus/v5`); pulled in
  because reimplementing the D-Bus wire format for one consumer
  was not the right complexity trade-off. Owns the login1 bus
  name and provides:

  - Manager: `GetSession`, `GetUser`, `GetSeat`,
    `GetSessionByPID`, `GetUserByPID` (with `/proc/PID/cgroup`
    fallback), `List{Sessions,Users,Seats,Inhibitors}`, session
    lifecycle (`CreateSession`, `ReleaseSession`,
    `ActivateSession`, `LockSession`, `UnlockSession`,
    `KillSession`, `TerminateSession`), user + seat lifecycle
    (`TerminateUser`, `TerminateSeat`, `KillUser`,
    `SetUserLinger`), device routing (`AttachDevice`,
    `FlushDevices`), the full 8-verb power family (`PowerOff`,
    `Reboot`, `Halt`, `Suspend`, `Hibernate`, `HybridSleep`,
    `SuspendThenHibernate`, `Sleep`) each with a `WithFlags`
    variant and matching `Can*` capability query,
    `ScheduleShutdown` + `CancelScheduledShutdown`, `Inhibit`,
    `Reload`. Sender-injected PID resolution (`dbus.Sender`
    magic-value + `GetConnectionUnixProcessID`) so callers
    passing `pid=0` (pam_elogind, older greeters) resolve to
    the caller.
  - Per-Session / per-User / per-Seat D-Bus objects with the
    full `loginctl` property surface (~25 per Session). Real
    `Session.TakeDevice` — resolves `M:m` via
    `/sys/dev/{char,block}/*/uevent` DEVNAME + hands the fd
    back over the D-Bus unix-fd wire.
  - Runtime layout follows the **elogind convention**, not
    systemd's: per-session cgroup scope at `/sys/fs/cgroup/<id>`
    (flat, NOT nested under `user.slice/…`) — libelogind's
    `sd_pid_get_session()` takes the FIRST path component of
    `/proc/PID/cgroup`, so systemd-style nesting would break it.
    Compat records land in `/run/systemd/{sessions,users,seats,
    machines}/` in shell-variable format so polkit's
    `PolkitUnixSession` and `pam_elogind` continue to work
    unmodified.
  - `/run/user/<UID>` created 0700 owned by the user;
    `/run/user` itself 0755 so the user can traverse into their
    own dir (a 0700 parent silently breaks XFCE menu discovery
    ~2-3 min into the session — caught the hard way).

  Elogind stays installed on target distributions purely for
  `libelogind.so.0` (dynamic dep of polkitd, gdm, pam_elogind,
  loginctl); only the **daemon** is replaced. Runtime cutover
  proven on Sunlight OS ceres — full XFCE stack, `loginctl`
  reports the expected session, `sd_pid_get_session()` returns
  `c2` for every user process, polkitd resolves subjects
  successfully without a live elogind.

- **`slinitctl status` / `show` / `status5` — systemctl parity**
  (~1.3k LOC across 7 commits). `slinitctl` now renders a
  status block that matches `systemctl status` line-for-line at
  the human level (Unit + Loaded/State/Since triple + Main PID +
  cgroup tree + last 10 journal lines), plus a `show` subcommand
  that mirrors `systemctl show`'s `Key=Value` dump — ~70 fields
  per service including live cgroup v2 accounting counters
  (`MemoryCurrent`, `MemoryPeak`, `CPUUsageNSec`, `TasksCurrent`,
  `IOReadBytes`/`WriteBytes`, `PSI-*`) read on demand from the
  service's scope. `status5` (the s6-style variant) gained
  Type + PID + `si_code`/`errno` symbol expansion, and a
  `-l/--long` flag that promotes it to the same `show`-backed
  Details block. `-l/--long` is now consistent across `status`,
  `status5` and `show`. New wire opcode `CmdServiceShow` (with
  the existing dinit-compat opcodes untouched).

- **`writePacket` write-deadline leak fix** (`pkg/control`,
  `5c73c95`) — the control-server writes a 5 s write deadline
  onto every response so a stuck reader can't wedge a handler
  goroutine indefinitely. Deadline is cleared with
  `SetWriteDeadline(time.Time{})` after each successful write
  and the connection is closed on timeout, matching the read
  side's semantics. Found via a deadline-audit sweep prompted
  by the `slinit-journalctl -f` hang fix in 2.3.0; no observed
  hang triggered the fix but the leak-on-timeout path was real.

### Added

- `cmd/slinit-logind/` — native login1 daemon. See Highlights.
  Ships as a standalone binary; no service file lands in the
  `slinit` package itself (packaging concern; Sunlight OS ships
  it via `sunlight-slinit-services` 1.4.0).
- `slinitctl status` gained: cgroup process tree (indented under
  the Main PID line) + last 10 journal lines (via
  `slinit-journalctl` — same query the operator would run by
  hand, filtered by the service's unit tag).
- `slinitctl show <svc>` — new subcommand. `systemctl show`
  analogue; dumps ~70 fields as `Key=Value` for machine
  consumption. Fields cover config surface (Type,
  ExecStart[Pre,Post,Reload], User, Group, RestartPolicy,
  RestartLimitBurst / IntervalSec, WorkingDirectory,
  environment sources, hardening knobs — DynamicUser,
  NoNewPrivileges, AmbientCap, RestrictNamespaces,
  CapabilityBoundingSet, ProtectHome, ProtectSystem,
  ProtectKernel*, PrivateDevices, PrivateTmp, SystemCallFilter,
  the full restrict-* set), runtime state (ActiveState,
  SubState, LoadState, TriggeredBy, Triggers, MainPID, ExecMain*,
  Result), and live cgroup v2 accounting counters read on demand
  from the service's scope.
- `slinitctl show -l/--long` — expands the standard field set
  with additional secondary fields (Slice, ControlGroup,
  ConditionResult, AssertResult, LimitCPU / LimitAS / LimitCORE
  / LimitDATA / LimitFSIZE / LimitLOCKS / LimitMEMLOCK /
  LimitMSGQUEUE / LimitNICE / LimitNOFILE / LimitNPROC /
  LimitRSS / LimitRTPRIO / LimitRTTIME / LimitSIGPENDING /
  LimitSTACK, TimeoutStartUSec / TimeoutStopUSec,
  StartLimitBurst / IntervalUSec, OOMPolicy, IOWeight,
  MemoryHigh / MemoryMax / MemorySwapMax, TasksMax). Same set
  gated by the flag across `status`, `status5` and `show`.
- `slinitctl status5` now shows Type + PID + last exit code
  with `si_code` and `errno` symbol expansion (`SI_KILL` /
  `SI_USER` / `EAGAIN`, not raw ints), and gained a
  `-l/--long` flag that appends a Details block sourced from
  `CmdServiceShow` (same content as `show`, indented inline
  under the status5 block).
- `CmdServiceShow` — new control-protocol opcode wrapping the
  show payload. Additive; existing opcodes unchanged.

### Fixed

- `pkg/control writePacket`: writes now apply a 5 s write
  deadline and close the connection on timeout, matching the
  read side. Prior code left the deadline unset, so a stuck
  reader on the control socket could park a server-side
  handler goroutine indefinitely — the read-side deadline audit
  that landed in 2.3.0's `slinit-journalctl -f` fix surfaced
  the same class of leak on the write side.
- `cmd/slinit` PID-1 hostname seeding: `os.Hostname()` at
  journal-ID init time was returning the kernel default
  `(none)` — `idCache.hostname` cached it for the rest of the
  boot, so every Load / Start / Stop event shipped to the
  journal tagged `(none)` even after a userspace early-setup
  service later called `hostname -F /etc/hostname`. Now
  seed `sethostname(2)` from `/etc/hostname` under `isPID1`
  before the journal cache primes. Gated on isPID1 so
  `--user` / `--container` invocations stay pure; missing or
  unreadable `/etc/hostname` is silently tolerated.
- `tests/functional/115-protect-hostname`: the Seccomp-install
  poll (added in `0b3f378`) held at 10x200ms which occasionally
  raced under full 218-case load — the sh child's
  `hostname()`/`sethostname()` slipped through to the host
  before the filter installed, mutating the host hostname for
  every subsequent case in the run. Bumped to 25x200ms (5 s)
  and added a best-effort restore-from-observed-value path so
  the harness self-heals when the race does fire (assertion
  still triggers, but the next case sees the original name
  instead of `functional-probe`).

### Changed

- `slinitctl status` output now includes cgroup tree + journal
  tail by default. `-l/--long` unchanged in scope but consistent
  in meaning across `status` / `status5` / `show`.
- `pkg/service.Show` renders the full expanded field set (~200
  fields with `-l/--long`, ~70 default). Prior default was ~40.

### Compat

- Wire protocol: `CmdServiceShow` added; existing opcodes
  unchanged. `slinitctl` 2.3.0 clients continue to work against
  a 2.3.1 daemon (they simply won't send the new opcode); a
  2.3.1 `slinitctl` falls back gracefully when talking to an
  older daemon that returns `NotSupported` for `CmdServiceShow`
  (renders the prior compact `status` block).
- Config surface: unchanged. No new directives, no removal.
- Cmdline: unchanged.
- Package manifests: `cmd/slinit-logind/` is a new binary. The
  slinit srcpkg does not yet list it in `go_package` (that bump
  waits for the next tag cut); packaging currently side-loads
  the binary via the sunlight-slinit-services 1.4.0 rootfs
  drop-in. The next tag cut should pick it up in `go_package`.
- Runtime deps: first non-`x/sys` runtime dep — `github.com/
  godbus/dbus/v5`. Vendored via `go.mod`; adds ~4 MB to the
  slinit-logind binary; no impact on the other cmd/ binaries.

## [2.3.0] — 2026-09-13

Milestone release — first minor bump on the 2.x line, marking the
end of the 2.2.x correctness / integration phase and the point at
which the current codebase has been continuously validated on real
desktop stacks (XFCE 4.20, GNOME 48, KDE Plasma 6, Cinnamon 6.6,
LXQt 2.4, MATE 1.26) with GDM3 and LightDM as PID-1 children,
soak-tested at 100+ back-to-back reboots on QEMU with zero
failed-start events, and shipped as an installable ISO
(sunlight-os) from a fresh live image.

No new commits vs 2.2.12 — this is a rename that consolidates the
work of the 2.2.x series into a version tag operators can pin to.
Everything that landed since 2.2.0 is now considered production-
ready on a laptop/desktop workload, not just a server one.

### Highlights of the 2.2.x → 2.3.0 arc (all shipped between
2026-09-06 and 2026-09-13)

- **finit-parity finalisation** (22 of 23 finit 5.0-rc1 items
  shipped) — `switch-root`, hardware-watchdog-driven reboot,
  `@console` sentinel, `slinit.cond=` mode selector, `hooks.d/*`,
  `/etc/rc.local`, `/etc/network/interfaces` bring-up,
  `slinit-getty` (agetty replacement, removes util-linux dep on
  embedded images), `slinit-watchdogd` (runtime WDT petting with
  `SIGPWR` handover). Two items deliberately deferred (D-Bus
  `org.finit` API, dlopen plugin ABI) with revisit triggers
  documented in README.

- **Boot-console cinematic UX** — auto-quiet on kernel cmdline
  `splash` / `slinit.quiet` (`pkg/bootmode` + `cmd/slinit`),
  banner suppression under the same trigger, catch-all mute
  post-boot, `OnShutdownAnnounce` un-mute so the shutdown WARN
  still reaches `/dev/console`. Plymouth spinner → GDM/LightDM
  handoff with zero text intermediate; ~400 ms userspace boot on
  KVM with the full graphical stack (dbus + elogind + polkitd +
  DM + docker + sshd + network).

- **`slinit-journalctl -f` no longer hangs after ~30 s**
  (`pkg/control/journal.go`) — the serve loop's 30 s read
  deadline leaked into the follow-mode `io.Copy(io.Discard,
  c.conn)` and orphaned the handler. Diagnosed via
  `/debug/pprof/goroutine` on a `-tags pprof` build showing the
  `handleJournalSubscribe` goroutine had already exited at the
  moment of the client-side stall.

- **`condition-file-value = PATH:VALUE`** native start predicate
  — reads a small sysfs file inline and compares to an
  operator-declared value, replacing the `pre-start-command =
  /bin/sh -c '...'` fork+exec pattern for gating on sysfs
  enums/bools (uart type, backlight state, thermal mode). Sub-µs
  vs 5-120 ms with high variance on cache-cold systems; ceres
  50-iter soak dropped the >400 ms userspace slow-tail from 34 %
  to ~2 %.

- **`log-forward-udp` standalone fix** — pre-existing regression
  from 2026-02-25 where the SyslogForwarder was only constructed
  inside the LogRotator branch (required a `log-file` to work).
  Services with just `log-forward-udp = host:514` silently
  dropped everything. New dispatch branch in `pkg/service/
  process.go` creates the forwarder + pipe + reader goroutine.

- **Test-harness robustness** — acceptance/69-escalating-shutdown
  no longer races the catch-all mute (uses `-B` to disable
  catch-all so the shutdown Notice always reaches stderr);
  functional/96-log-forward-udp respawns BusyBox `nc` between
  self-test and the assertion so the sticky-connect quirk does
  not swallow subsequent datagrams; `tests/performance/demo/cold-
  boot.sh` fixed a bimodal +1 s spike from a poll-vs-event race
  in `runit-svc` (`ready-check-interval` 1 s → 100 ms).

- **Sunlight OS integration proven** — slinit runs PID 1 on
  Sunlight's live ISO + installed disks with slpkgs-native
  packaging (`slinit`, `slinit-openrc-shims`, `sunlight-slinit-
  services`), booting into a full graphical session (any of the
  DEs above) with lightdm/gdm as slinit-managed child services.
  100-boot QEMU soak with zero failed-start events.

### Compat

- Wire protocol: unchanged from 2.2.x (CPVersion=7,
  MinCompatVersion=1). `slinitctl` 2.2.x clients continue to
  work against a 2.3.0 daemon; a 2.3.0 `slinitctl` continues to
  work against a 2.2.x daemon.
- Config surface: `condition-file-value` is a new opt-in
  directive; a 2.2.x parser will simply not recognise it (silent
  skip). Existing service files are unchanged.
- Cmdline: `splash` and `slinit.quiet` are new opt-in triggers;
  absent, behaviour is unchanged.
- Package manifests: no rename or removal on the `cmd/` list; no
  binary was retired since 2.2.10.

## [2.2.12] — 2026-09-13

Cinematic-boot polish + one small feature. Three commits, no
behaviour change on stock installs (all three trigger only on
opt-in cmdline args or opt-in service directives), no wire
protocol change, no config surface removal.

### Added

- `condition-file-value = PATH:VALUE` — new native start
  predicate that reads a small file (4 KB cap so a bad path
  cannot stall boot on a growing log), trims trailing whitespace
  including CR/LF, and string-compares the content to the
  operator-declared value. Combined with the existing
  leading-`!` negation (`Predicate.Negate`), covers both
  "content equals X" and "content does NOT equal X" without
  the `pre-start-command = /bin/sh -c '...'` fork+exec that was
  the only prior workaround. Sub-microsecond vs 5-120 ms with
  high variance on cache-cold systems. Distinct from
  `condition-path-exists` (sysfs entries exist unconditionally
  for phantom slots), `condition-file-not-empty` (the string
  "0" is a non-empty file), and `condition-kernel-command-line`
  (different source). Typical use: gate a service on a sysfs
  enum/bool like `/sys/class/tty/ttyS0/type` (0 = phantom ISA
  slot, nonzero = real UART), `/sys/class/backlight/*/actual_
  brightness`, or `/sys/class/thermal/thermal_zone*/mode`.

  Measured on ceres (identical 50-iter soak, kernel 7.2.3):
  ```
  Before (shell pre-start): userspace p90 = 403 ms, slow-tail 34 %
  After  (native predicate): userspace p90 = 404 ms, slow-tail  2 %
  ```
  Median unchanged; the win is on the tail where the shell
  fork used to spike.

### Changed

- Cmdline `splash` and `slinit.quiet` now auto-flip slinit
  into quiet mode (`Quiet` in `pkg/bootmode`; suppressed
  `bootConsole`, `logger.SetLevel(LevelError)`,
  `PrintBootBanner` no-op). Rationale: any operator setting
  `splash` on the kernel cmdline has already told the system
  "I want a graphical boot"; the `[OK]/[FAIL]` cascade
  slinit was still writing to `/dev/console` fought Plymouth
  for the framebuffer and lost, and the `slinit booting...`
  banner did the same flash. Two clean triggers, either
  sufficient. Backward-compatible: absence of both leaves
  every existing setup at the verbose console it had.
  `slinit.debug` still wins when both are set — an operator
  debugging boot gets the verbose stream regardless of the
  splash marker.

  Removes the need for the wrapper-script pattern
  (`init=/usr/local/sbin/slinit-quiet` → `exec /usr/bin/slinit
  -q "$@"`) that operators had to install manually to get a
  clean splash. Now `init=/usr/bin/slinit quiet splash` on the
  GRUB cmdline is the whole story.

## [2.2.11] — 2026-09-12

Single-fix patch release: `slinit-journalctl -f` hanging after
~30s of stream time. Not a regression from v2.2.10 — the bug has
been latent since v2.1.10 landed the follow-mode dispatcher; it
just surfaced now because the debug session sat on `-f` long
enough to trip the deadline. No behaviour change beyond making
follow actually follow.

### Fixed

- `pkg/control`: `slinit-journalctl -f` orphaning its
  server-side handler after exactly 30s. The dispatch loop in
  `(*Connection).serve` stamps a 30-second read deadline before
  every `ReadPacket` so it can re-check `ctx.Done` on a wedged
  connection — that deadline is correct for the
  request/response commands the dispatch loop was written for,
  but it leaked into `handleJournalSubscribe`. Follow sessions
  are one-way (the client sends nothing after the initial
  `CmdJournalSubscribe` payload), and the handler starts an
  `io.Copy(io.Discard, c.conn)` goroutine to detect client
  disconnect via read EOF. With the deadline armed, that Read
  tripped after 30s with a timeout error, closed the internal
  `done` channel, the for-select in the handler hit `<-done`
  and returned nil, and the connection went orphan server-side:
  handler gone, socket still open, no more `RplyJournalEntry`
  packets ever sent. The client sat forever on `ReadPacket`
  waiting for events that would never come and the CLI
  appeared to hang. Fix (~20 lines in
  `pkg/control/journal.go`): clear the read deadline at the
  top of `handleJournalSubscribe`
  (`SetReadDeadline(time.Time{})`) so `io.Copy` blocks
  indefinitely until the client actually closes its end. The
  serve loop re-arms the deadline on its next iteration, which
  subscribe never reaches — the handler is terminal for the
  connection. Diagnosed via `/debug/pprof/goroutine` on a
  `-tags pprof` slinit build, which showed
  `handleJournalSubscribe` had already exited at the moment the
  client stall was visible.

  Verified live on ceres (kernel-cmdline spamming
  `/dev/kmsg` every 200ms):

  ```
  Before:  t=30s lines=4301 last=kmsg-145
           t=45s lines=4301 last=kmsg-145   ← stalled
  After:   t=30s lines=4307 last=kmsg-150
           t=45s lines=4387 last=kmsg-...   ← still growing
  ```

## [2.2.10] — 2026-09-11

Finit-parity finalisation + boot-console UX polish + one
pre-existing regression closed. The Finit 5.0-rc1 feature
comparison now stands at **22 of 23 items shipped**; the two
that remain (finit's `org.finit` D-Bus control API + dlopen
plugin ABI) are deliberate non-goals with a documented
"revisit-if-adoption-demands" note in README, not gaps waiting
for effort.

Interactive-boot UX rework: the catch-all logger's console tee
was quietly making the login prompt fight for the serial line
on any host with chatty stdout services (demo VM's 34-service
tree exposed it hard); a post-boot mute plus a shutdown-time
un-mute keep boot progress + STOPPD cascade visible while
removing the runtime spam that races bash's typing echo.

A `log-forward-udp` bug that had been latent since the LogRotator
guard landed 2026-02-25 finally surfaced through functional test
96 — a service declaring `log-forward-udp` without any of the
LogRotator-triggering directives (log-type, log-file, …) never
built a SyslogForwarder, so UDP forwarding was silently dead.
Fixed alongside a related "PRI = -2" wire error when no
`log-forward-facility` was set.

### Added

- **`cmd/slinit-getty` — minimal built-in login-prompt binary.**
  Closes the last partial GAP from the Finit inventory (#15).
  Byte-for-byte argument compat with finit's `getty` tool
  (`slinit-getty [-p] TTY [BAUD [TERM]]`) so existing tty
  service entries transfer cleanly. Flow matches finit's
  src/getty.c: setsid → open TTY → TIOCSCTTY (force=1) →
  termios canonical + ECHO + ONLCR + ICRNL + 8N1 → render
  `/etc/issue` with the classic `\d \l \m \n \o \r \s \t \u
  \v` escapes (unknown escapes passed through so ANSI
  survives) → prompt → read username → execve `/bin/login
  [-p] -- USER`. Fallback chain if `/bin/login` is missing:
  `/sbin/login → /usr/bin/login → /sbin/sulogin → /bin/sulogin
  → /bin/sh` — an operator without a proper login binary
  still gets an interactive shell instead of a boot-time
  hang. Reduces util-linux dependency on embedded / BusyBox
  images. 404 LOC + 145 LOC tests + 147 LOC man page.

- **`cmd/slinit-watchdogd` — runtime hardware watchdog petting
  daemon.** Closes the last partial GAP from the Finit
  inventory (#16 / #22). Opens `/dev/watchdog`, sets the
  reset countdown via `WDIOC_SETTIMEOUT`, and calls
  `WDIOC_KEEPALIVE` at half the timeout interval. Complements
  `pkg/shutdown`'s existing `slinit.reboot-watchdog` (which
  arms the WDT at shutdown for a hardware reset): together
  they cover the full runtime-plus-shutdown WDT lifecycle a
  real embedded deployment needs.

  Signals:
  - `SIGTERM` / `SIGINT`: write `V` magic-close byte + close
    → graceful disarm (WDT does NOT fire).
  - `SIGPWR`: close without magic byte → WDT stays armed for
    a successor daemon to inherit (finit-parity handover).
  - `SIGHUP`: re-arm at current timeout without restart.

  Defaults: 60 s timeout, timeout/2 interval, clamped to
  `[5s, timeout-1s]` so the WDT sees ≥ 2 pets per window
  minimum. Verbose (`-v`) logs every pet at Info; quiet by
  default. 224 LOC + 87 LOC tests (ioctl constants locked
  against `linux/watchdog.h` encoding) + 156 LOC man page.

### Fixed

- **`pkg/service`: `log-forward-udp` works standalone (no
  `log-file` requirement).** Pre-existing regression from
  2026-02-25 (commit `a8bf4456`) — the SyslogForwarder was
  constructed only inside the LogRotator branch, which
  itself required `logType == LogToFile && logFile != ""`.
  A service declaring only `log-forward-udp` (no log-type,
  no log-file) fell through every dispatch case with
  `outputPipe = nil`; the child inherited slinit's stdout
  and no UDP datagram was ever emitted. Functional test 96
  silently SKIP'd for months because BusyBox nc dropped UDP;
  a newer Alpine image accepted UDP and exposed the bug.

  New dispatch branch in `pkg/service/process.go`
  (`else if s.logForwardUDP != ""`) creates a fresh pipe +
  SyslogForwarder + line-reader goroutine that ships every
  stdout+stderr line via `fw.Send`. Zero disk cost. Post-fork
  closes parent's copy of the pipe write end; goroutine sees
  EOF once the child exits. Lifecycle managed via new
  `syslogForwarderOnly *SyslogForwarder` field with cleanup
  in `stopLoggerCommands` + error-path teardown.

- **`pkg/config`: `SyslogFacilityCode("")` defaults to
  LOG_USER instead of returning `-1`.** With no explicit
  `log-forward-facility` directive, `desc.LogForwardFacility`
  is empty; the code returned `-1` for "not found in map",
  callers computed `pri = -1*8 + severity = -2`, wire output
  was `<-2>UDPFWD_MARK` — RFC 3164 rejects PRI outside [0,
  191], no conformant syslog receiver accepts a negative
  value. Empty facility now returns `1` (LOG_USER, standard
  general-userspace default); observed PRI is `<14>` for
  default facility + info severity (1*8 + 6).

- **`pkg/eventloop`: `OnShutdownAnnounce` hook — un-mute
  catch-all before shutdown WARN.** Follow-up to the
  post-boot catch-all mute below (which correctly silences
  runtime service echoes but incorrectly also hid the
  operator-visible `Shutting down slinit (kind)...` WARN
  line + subsequent `[STOPPD]` cascade because the WARN
  fires inside `initiateShutdown` BEFORE any `OnPreShutdown`
  callback runs). New callback at the top of
  `initiateShutdown` gives cmd/slinit main a seam to call
  `SetConsoleMuted(false)` before anything logs. Symmetric
  console-mute lifecycle: boot phase = mute OFF ("[ OK ]
  name" visible); OnBootReady = mute ON (runtime silent);
  OnShutdownAnnounce = mute OFF ("Shutting down..." +
  "[STOPPD] cascade" visible); reboot syscall = dead.

- **`tests/functional/cases/96-log-forward-udp.sh`: respawn nc
  between self-test and real assertions.** BusyBox nc `-u -l`
  (UDP listen) implicitly connects to the source of the first
  datagram it receives; the self-test probe's ephemeral
  source port isn't slinit's forwarder source port, so
  subsequent datagrams get silently dropped. Test harness
  now kills + respawns nc after a passing self-test, giving
  slinit's forwarder a fresh listener to bind against. No
  slinit-side change — the standalone log-forward-udp path
  was correct; the harness was hostile to it.

- **`demo/services/runit-svc`: `ready-check-interval` 1s →
  100ms — cold-boot bimodal spike gone.** The demo cold-boot
  benchmark showed a 3-of-10 iterations spike at ~+1000 ms
  (median 2780 ms, p95 3790 ms). Root cause: runit-svc
  sleeps 2 s in its command before creating
  `/run/runit-svc.ready`; slinit's poll fires every 1 s.
  Classic poll-vs-event boundary — if the poll happened
  before `touch`, the file was caught on the NEXT poll a
  full 1 s later. Reduced interval to 100 ms — median
  unchanged (2 s app-warmup is intentional), p95 tightened
  to 2810 ms (30 ms above median, distribution collapsed
  to a 60 ms window across all iterations). Not a slinit
  runtime bug; a demo-service configuration choice.

### Changed

- **`pkg/logging`: catch-all console tee muted post-boot.**
  Prior behaviour teed every captured stdout+stderr line to
  both `/run/slinit/catch-all.log` AND the original console.
  On demo boots with 40+ services running echo-loops, that
  became a continuous stream of console writes racing bash's
  line-echo for the ttyS0 serial line — interactive typing
  felt laggy because the kernel serial buffer was
  perpetually flushing service output.

  New `CatchAllLogger.SetConsoleMuted(bool)` toggle backed
  by `sync/atomic`. cmd/slinit main flips it on inside the
  OnBootReady wrapper: boot phase keeps its console mirror
  (operator sees `[ OK ] name` stream), runtime goes silent
  on console but file still receives everything for
  post-hoc grep via `slinitctl catlog` / `slinit-journalctl`.
  See the OnShutdownAnnounce fix above for the symmetric
  un-mute at shutdown.

- **`tests/performance/demo`: cold-boot harness dumps
  per-service timing on spike iterations.** `perf_write_
  collector` now embeds a `BOOT-TIME-BEGIN..END` block with
  `slinitctl boot-time` output; `cold-boot.sh` auto-prints
  it for iterations exceeding `COLD_BOOT_SPIKE_MS` (default
  3000 ms) or when `VERBOSE=1`. Landed the runit-svc fix
  above; instrumentation stays for future perf
  investigations.

- **`README.md`: finit bullet expanded to full 22-item
  inventory + deferred-by-design note.** Prior version
  listed 4 items (switch-root, reboot-watchdog, @console,
  boot-cond) missing 8 subsequent additions. Now enumerates
  every shipped finit-parity feature plus a paragraph naming
  the two deliberate deferrals: `org.finit` D-Bus API
  (positioning stays Unix-socket-first; a D-Bus-driven admin
  tool can shell to `slinitctl` via a small wrapper) and
  dlopen plugin ABI (Go's static-linking model makes a
  stable C-ABI surface expensive; existing hooks.d + env-
  generator cover most operator customisation without the
  maintenance burden). Neither closes the door — both are
  "revisit if adoption demands" (concrete external ask
  reopens the conversation).

## [2.2.9] — 2026-09-10

Finit-parity release. A fresh look at [finit](https://github.com/troglobit/finit)
5.0-rc1 as a seventh upstream surfaced nine actionable items;
eight ship here (one — the `conflicts:` directive — deliberately
skipped: no user demand, no equivalent in dinit / runit / OpenRC,
and a real implementation costs 3-5× the audit's estimate once
state-machine integration is priced in). The batch pulls in
several long-missing ergonomics: initramfs → real-root
switch-root, boot-mode selection via kernel cmdline, legacy
`/etc/rc.local` + Debian-style `/etc/network/interfaces`
integration, and a small pkg/hooks framework that gives operators
a scriptable extension point at every lifecycle boundary. Also
folds in one fuzz-found `makeslice` panic (Xeon 40-thread run)
and closes a dinit-migrant UX seam.

### Added

- **`slinitctl switch-root NEWROOT [INIT]`** — new pkg/switchroot
  package + control opcode `CmdSwitchRoot = 64`. Enables slinit
  as an initramfs init: the initramfs does the platform-specific
  pre-mount work (decrypt LUKS, assemble LVM, wait for NBD /
  iSCSI), then hands off to slinit-in-newroot via a control-
  socket call. Precheck validates the request cleanly before
  the point of no return (PID 1, newroot exists + is a real
  mount point, init file exists / executable / regular).
  Actual switch stops services, kills processes, moves /dev, /
  proc, /sys, /run into the new root via `MS_MOVE`, deletes the
  initramfs contents when `/` is ramfs / tmpfs, chroots, and
  `execve`s the new init with PID 1 preserved. finit-parity for
  `initctl switch_root`.

- **`condition-boot-cond = <name>`** service directive +
  `slinit.cond=foo,bar` kernel-cmdline selector. Enables
  factory / upgrade / provisioning boot modes without editing
  service files. `slinit.cond=factory` sets one tag,
  `slinit.cond=a,b,c` sets three; multiple `slinit.cond=` tokens
  are merged. `finit.cond=` also honoured as a parity shim for
  operators with an unchanged finit boot cmdline. Reuses the
  existing predicate framework — 8 tests, all safe for CI.

- **`slinit.reboot-watchdog` kernel-cmdline** — arms `/dev/
  watchdog` with a short timeout (10 s default, clamped [1, 300]
  s) before `reboot(2)` on ShutdownReboot. For embedded boards
  whose SoC reboot syscall is unreliable (bootrom quirks,
  partial GPIO state) but whose WDT peripheral always produces a
  clean reset. Uses raw `syscall.Open` + `WDIOC_SETTIMEOUT`
  ioctl; fd stays open (no "V" magic close) so drivers without
  `CONFIG_WATCHDOG_NOWAYOUT` don't disarm on close. Falls
  through to `reboot(2)` if open or ioctl fails.

- **`slinit.reboot-delay=<N>` kernel-cmdline** — inserts an
  N-second pause between the shutdown-hook and the `reboot(2)`
  syscall. Clamped to [0, 60]. For boards whose reset path
  needs quiescent-bus time or external hardware needs to see
  the "shutdown initiated" signal before taking over.

- **`slinitctl suspend [STATE]`** — new opcode `CmdSuspend = 65`
  + CLI subcommand. Writes STATE (default "mem") to
  `/sys/power/state`; validates against the kernel's own
  {freeze, standby, mem, disk} whitelist AND against the
  runtime supported-list before the write, so unsupported
  targets produce a clear error instead of an opaque `EINVAL`.
  Blocks the client until wake (freeze / standby / mem).
  finit-parity for `initctl suspend`.

- **`slinitctl edit NAME`** — client-only helper (no new
  opcode). Resolves the on-disk service description via
  `CmdQueryServiceDscDir` + `CmdLoadService`, opens it in
  `$VISUAL` / `$EDITOR` / `vi` (systemctl edit precedence),
  triggers a reload on successful editor exit. Non-zero editor
  exit aborts without touching the daemon — safer than the
  shell idiom `vi FILE && slinitctl reload NAME` which fires
  reload on any zero exit including `:q!` saves. finit-parity
  for `initctl edit NAME`.

- **`tty-path = @console` sentinel** — resolves at service-
  start time to the last entry in
  `/sys/class/tty/console/active` (the tty `/dev/console`
  redirects to). One service definition boots the right getty
  across VGA and serial installs of the same image. Fails
  loudly when sysfs is unmounted or the file is empty — no
  silent fallback to the wrong device.

- **`pkg/hooks` — operator-hook scripts at
  `/etc/slinit/hooks.d/<point>/*`**. Executable files in the
  per-point subdirectory run in name-sorted order, one at a
  time, 30 s per-script timeout. Non-executable files are
  skipped silently. Failures are logged but non-fatal (hooks
  are best-effort augmentation, not gates). Points fired:
  **system-up** when the boot service reaches STARTED,
  **system-down** at the top of shutdown before teardown,
  **switch-root** at the top of the initramfs transition.
  Script output routed through slinit's Info-level logger so
  it doesn't race the console with the interactive tty.
  `SLINIT_HOOK_POINT` exposed in the child env so a shared
  script can branch on invocation path.

- **`/etc/rc.local` + `/etc/rc.local.d/*` (finit-parity
  runparts)**. Zero-config end-of-boot escape hatch for legacy
  SysV / Debian / Alpine / Slackware operators. If executable,
  run once at end-of-boot right after the `system-up` hook
  point. `/etc/rc.local.d/*` drop-ins fire first (matching
  Debian rc-local.service convention), then the monolithic
  script. 5-minute per-script timeout, `SLINIT_HOOK_POINT=
  rc-local` in the env.

- **Debian / BusyBox `/etc/network/interfaces` integration** —
  new `pkg/network` with two functions: `BringUpLoopback()`
  (idempotent `SIOCSIFFLAGS` ioctl on `lo`, brought up at
  very early boot so daemons binding to `127.0.0.1` don't
  wait for an operator-declared network service) and
  `RunIfup(up, logger)` (fork+exec `ifup -a` at boot end,
  `ifdown -a --force` / `-a -f` at shutdown, honouring
  `ifquery` presence to distinguish Debian ifupdown from
  BusyBox). Silent no-op when either prerequisite is missing
  — hosts using networkd / dhcpcd / NetworkManager / manual
  services aren't touched. 90 s per-invocation timeout.
  finit-parity for `void networking(int updown)` in finit's
  `src/helpers.c`.

- **`no-boot-marker = yes` directive** — suppresses the
  `[ OK ] name` boot-console line for milestone-style services
  whose completion is implied by the tree underneath (a
  `depends-on: tty` milestone reached STARTED after bash
  spawned a login prompt would otherwise print its marker
  after the prompt, cluttering the console). Main log still
  records the transition.

- **Server-side shutdown announcement on `/dev/console`**. On
  every shutdown path (control socket, slinit-shutdown binary,
  SIGINT/SIGTERM relay, `reboot(2)`-syscall trigger), slinit
  now emits `[HH:MM:SS] WARN: Shutting down slinit (kind)...`
  before the `[STOPPD]` cascade begins. Level is Warn so it
  clears the boot-console filter that `cmd/slinit` sets to
  LevelWarn in systemMode — Notice would be silently dropped
  there, defeating the point of an operator-visible
  announcement.

- **`pkg/shutdown` shutdown-hook fallback for dinit
  migrants.** Slinit's shutdown-hook lookup now also checks
  `/etc/dinit/shutdown-hook` and `/lib/dinit/shutdown-hook`
  after its own paths, mirroring the `/etc/dinit/environment`
  fallback already wired into `cmd/slinit/main.go`. A
  dinit → slinit migration keeps a legacy hook working
  silently. Slinit-native paths still win when both exist.

- **README lists finit as a seventh upstream** alongside
  dinit, runit, s6-linux-init, OpenRC, upstart, systemd.
  Per-upstream section names the concrete features slinit
  carried over.

- **Demo integrations**: three new artefacts under `demo/` so a
  fresh QEMU boot exercises the new surfaces without editing
  anything:
  - `services/factory-mode-demo` — boot-cond gate, STOPPED
    unless the operator boots with `slinit.cond=factory`.
  - `services/tty-autoconsole` — @console sentinel demo,
    manual so it doesn't fight the interactive tty.
  - `hooks.d/{system-up,system-down}/50-*` — two
    echo-marker scripts.
  - `/etc/rc.local` marker script installed by `build.sh`.

### Fixed

- **`pkg/journalbin`: DATA/ENTRY object size bounds checked
  before `makeslice`.** Fuzz found a crash in
  `readDataPayloadAt` after 82 s / 1.8 M execs on a 40-thread
  Xeon: a hostile item pointer directing at a DATA header with
  `Size` approaching `uint64.Max` made `payloadLen = oh.Size -
  dataFixed` remain huge, and `make([]byte, payloadLen)`
  tripped Go's `makeslice: len out of range` panic — process
  termination, kernel panic if PID 1. Iter already had an
  overflow-safe check for its top-level walk (v2.2.6); this
  extends the same pattern to `readDataPayloadAt` (called via
  ENTRY item pointers that Iter doesn't validate individually)
  and `ReadEntryAt` (public path, raw caller-supplied offset).
  Reader now caches file size at Open + a shared
  `checkBounds(off, size)` helper enforces overflow-safe
  bounds. Regression tests reproduce the crash shape (DATA
  header with `Size = 1<<62`) + the past-EOF path.

- **`slinit-journald` service teardown honours
  `slinit.reboot-watchdog`** — the WDT-arm path is guarded by
  `rebootType == ShutdownReboot` so poweroff / halt /
  softreboot / kexec fall through untouched.

### Changed

- **Boot-console UX: login prompt now lands on its own line.**
  Prior demo boot output showed hook / rc.local / ifup
  wrapper output interleaving character-by-character with
  bash's prompt on `/dev/console`. Three layers of change
  land the fix:
  1. Hook / rc.local / ifup stdout+stderr are captured
     through slinit's Info-level logger instead of raw
     `os.Stdout`. Info is below the boot-console filter
     threshold (Warn), so script markers land in the journal
     — `slinitctl catlog` and `slinit-journalctl` surface
     them post-hoc — but no longer race the console.
  2. Child process groups (`Setpgid = true` + `cmd.Cancel =
     kill(-pid, SIGKILL)` + 2 s WaitDelay) — orphaned
     grandchildren holding the stdout pipe past deadline
     can't wedge cleanup. Cost: three extra lines per exec
     site; benefit: `dhclient` / `network-restart` scripts
     that background subshells no longer hang the boot.
  3. `pkg/hooks` and `pkg/network` tolerate `ECHILD` on
     `cmd.Wait()`: slinit-as-PID-1 generic-reaps every
     zombie, and pipe-drain-then-Wait widens the race window
     where the reaper wins. Under the best-effort hook
     contract, ECHILD is treated as success. Exit status is
     lost, but hook failures never gated boot anyway.

- **`shutdown.Execute` gains an operator-visible
  `Shutting down slinit (kind)...` line** at Warn level
  ahead of the `[STOPPD]` cascade — served by
  `initiateShutdown` in `pkg/eventloop`.

- **`slinit-journalctl` + Reader tolerate `ECHILD`** — no
  spurious "no child processes" warnings on hook / rc.local
  / ifup child completion in PID-1 mode.

## [2.2.8] — 2026-09-08

Documentation + upstream-parity pass. Closes the two loose ends
surfaced during the v2.2.7 sign-off: nine slinit binaries under
`cmd/` had shipped without man pages, and three of the five new
dinit upstream state-machine commits (`24fb3d8..bf63b44`) needed
porting. No user-facing regressions or new capabilities; the
state-machine ports harden consistency corners that were latent
in v2.2.7 rather than observably broken. Ship-worthy on the
strength of the doc completeness alone.

### Added

- **Nine new man pages closing the binary → doc gap.** Every
  binary under `cmd/` now has a source `.md` in `doc/man/`:

  - `slinit-supports.8` — self-introspection CLI, source of
    truth for `doc/features.md`.
  - `slinit-journalctl.8` — 65-flag systemd-parity journal CLI
    (441 lines, biggest single page; 16 flag groups from Event
    selection through FSS + Catalog + Machine target).
  - `slinit-journald.8` — persistent journal daemon documenting
    both Phase B binary and Phase C JSONL formats.
  - `slinit-journal-migrate.8` — one-shot JSONL → binary
    migrator.
  - `slinit-machinectl.8` — nspawn container registry query /
    CRUD (`/run/slinit/machines/` file format documented).
  - `slinit-nspawn.8` — nspawn container runtime, contrasted
    explicitly with `systemd-nspawn(1)`.
  - `slinit-openrc-convert.8` — `init.d` → `slinit-service(5)`
    converter.
  - `slinit-runit-convert.8` — runit svdir →
    `slinit-service(5)` converter.
  - `slinit-systemd-convert.8` — systemd `.service` →
    `slinit-service(5)` converter (other unit types documented
    as out of scope).

  `doc/man/Makefile` `PAGES_8` list extended with the nine new
  targets; all pages render cleanly via `make -C doc/man` (`go
  tool github.com/cpuguy83/go-md2man/v2`) and install under
  `$(MANDIR)/man8/`. +1377 lines. `slinit-hostnamectl` /
  `slinit-timedatectl` remain covered by the existing
  `hostnamectl.1` / `timedatectl.1` pages via install-time
  symlinks.

### Fixed

- **`pkg/service`: `queueForConsole` guards against
  double-enqueue (dinit `a000e76`).** Prior code unconditionally
  set `waitingForConsole = true` and appended `self` to the
  console queue. If `allDepsStarted` re-entered while the record
  was still waiting for the console (a dep transition firing the
  readiness check twice before `AcquiredConsole` ran), the same
  `ServiceRecord` landed on the console queue twice — the second
  `PullConsoleQueue` would then dispatch a service that had
  already released the console. Fix skips the append when the
  flag is already set. Latent in v2.2.7; ported defensively.

- **`pkg/service`: `startCheckDependencies` requires
  `waitingForDeps` before flagging a dependent as `WaitingOn`
  (dinit `5fe1081`).** The dependents loop marked
  `dept.WaitingOn = true` whenever the dependent was
  `StateStarting`; without the extra `waitingForDeps` check, a
  dependent that had already resolved its deps but was in the
  console-acquisition branch picked up a stale WaitingOn flag
  pointing at a dependency that just finished starting. No
  observable misbehaviour in v2.2.7, but state now stays
  authoritative for outside observers.

- **`pkg/service`: `ExecuteTransition` short-circuits when
  `waitingForDeps` is already clear + `allDepsStarted` clears
  the flag before the console-queue branch (dinit `d24e6f9`).**
  Two linked changes that close the loop on the fix above: once
  a record has reached `allDepsStarted`, `waitingForDeps`
  becomes authoritative and re-entries don't have to walk every
  dep to reach the same conclusion. Clearing the flag before the
  `queueForConsole` branch (instead of after) means the
  ordering-only-dependent check from `5fe1081` sees the correct
  post-resolution state instead of a stale `true` for services
  that took the console-queue path.

  The remaining two upstream commits (`bf63b44` `interrupt_start`
  rename + `76a50f6` C++ header comment fix) are N/A: slinit
  already uses the target names (`InterruptStart` /
  `CanInterruptStart`), and the sole caller of `CanInterruptStart`
  at `record.go:3061` already guards with `!waitingForDeps &&
  !waitingForConsole`.

  All 29 `pkg/*` test packages pass under `-race -count=1` before
  and after both ports.

### Changed

- **Docs currency pass across 50+ `.md` files.** Test-count
  headers refreshed everywhere they appeared (unit
  `1956 → 2033`, `_test.go` files `273 → 291`, Go dirs
  `69 → 65`, acceptance `218 → 219`, fuzz targets `21 → 27`) in
  `README.md`, `CLAUDE.md`, `CONTRIBUTING.md`. `SECURITY.md`
  "Supported Versions" table refreshed (`>=2.2.0` supported;
  `2.0.x`/`2.1.x` best-effort; `<2.0.0` unsupported).
  `doc/features.md` header pins the exact regeneration command
  (`slinit-supports --list-directives --group-by=source`).
  Fuzz corpus size line updated ("54 files as of v2.2.7").
  `tests/performance/README.md` + `tests/performance/ssh/
  README.md` dropped the "not yet populated" stubs and now
  reflect the shipped v2.2.7 harness (4 demo harnesses + 93 SSH
  cases; first ceres measurement in v2.2.7).
  `demo/README.md` service table gained the
  `persist-journal-mount` row.

- **`.github/pull_request_template.md`**: expanded testing
  checklist (unit + functional + acceptance rows), dinit-parity
  item, DCO reminder, `CHANGELOG.md`-update reminder.
  **`.github/ISSUE_TEMPLATE/feature_request.md`**: added an
  "Upstream Parity" section so requesters name any
  dinit / systemd / runit / s6 / OpenRC equivalent up front.
  **`.github/ISSUE_TEMPLATE/bug_report.md`**: version example
  bumped `v2.0.0 → v2.2.7`.

## [2.2.7] — 2026-09-06

Critical PID-1 fix + optional pprof diagnostic endpoint + massive
perf-test expansion. The fix stops slinit from kernel-panicking
the host when multiple control-socket clients concurrently load
distinct services — a class of bug that only surfaces under
stress but is fatal when it does. The pprof endpoint (build-tag
gated so stock builds pay zero cost) plugs slinit into the
standard Go profiling toolchain for future leak / hotspot
investigations. The perf work extends `tests/performance/ssh/`
from 14 to 93 SSH-driven cases and produces the first
comprehensive coverage of slinit's control-surface hot path.

### Fixed

- **`pkg/config`: DirLoader now serialises concurrent
  LoadService calls.** Root cause of a kernel panic surfaced
  by stress testing on ceres (2026-09-05):
  `DirLoader.loading map[string]bool` + `DirLoader.curDepth int`
  were mutated by `loadServiceImpl` (`loader.go:671/677/678`
  pre-fix) without any mutex. Two goroutines each servicing a
  control-socket LoadService request race on the map. Go's
  runtime detects concurrent map read+write and terminates the
  process with `fatal error: concurrent map read and map write`
  — unrecoverable. When slinit is PID 1, kernel panics.

  Reproduces in 9 ms with 32 goroutines calling LoadService on
  distinct services (regression test
  `TestDirLoader_ConcurrentLoadService`); reproduces on the
  live VM within one iteration of
  `tests/performance/ssh/cases/580-parallel-lifecycle-4.sh`.

  Fix: `sync.Mutex` on `DirLoader`, taken by the public
  `LoadService` and `ReloadService` entry points; internal
  recursion through `loadDependencies` migrated from
  `dl.LoadService` to a new `loadServiceLocked` bypass so a
  single goroutine holds the lock through the entire dep-tree
  load without self-deadlocking. Four recursive callers
  updated (two in loadDependencies + producer + logger
  lookups). LoadService is not on a hot path — full mutex
  serialisation adds no measurable overhead but rules out
  the whole class of concurrent-map bugs in the loader.

### Added

- **Optional `net/http/pprof` endpoint on `/run/slinit/pprof.sock`,
  guarded by the `pprof` build tag.** Stock builds compile a no-op
  stub (`cmd/slinit/pprof_stub.go`, 7.7 MB binary, zero
  runtime surface). Diagnostic builds with `-tags pprof`
  compile the real handler (`cmd/slinit/pprof.go`, +5 MB
  binary for the net/http + pprof surface, socket chmod 0600
  root-only). Two-file layout means the diagnostic can be
  resurrected any time by rebuilding with the tag — no code
  changes required.

  Rebuild + hot-patch flow (~2 minutes):

  ```
  go build -tags 'paniconce pprof' -ldflags='-s -w' \
      -o /tmp/slinit ./cmd/slinit
  scp /tmp/slinit target:/usr/bin/slinit.new
  ssh target 'mv /usr/bin/slinit.new /usr/bin/slinit && \
              slinitctl shutdown softreboot now'
  # PID 1 picks up new binary; socket at /run/slinit/pprof.sock
  ssh target 'curl --unix-socket /run/slinit/pprof.sock \
      http://x/debug/pprof/heap' > before.pprof
  # ... induce load ...
  go tool pprof -diff_base before.pprof after.pprof
  ```

  First use of this endpoint closed the RSS-growth observation
  from perf case `141`: live heap unchanged at 4661 kB across
  12500-op load window, goroutine count constant at 37 —
  confirmed the observed +2.7 MB RSS delta is Go runtime heap
  arena, not a leak.

- **`tests/performance/ssh/` expanded from 14 to 93 cases.**
  Comprehensive coverage of slinit's control-surface hot path:
  all slinitctl read + write ops, journal read/write/filter
  variants, concurrent client scaling (1→128), lifecycle
  scaling curve (1/2/4/8/16/20-way), long-tail latency,
  reader-writer racing, cross-service concurrency, negative
  paths, OpenRC compat shims, specialised directives (PSI /
  cgroup / fd-store), parser stress (50-directive svc, error
  path), and dep-tree scaling (10-node + 100-node chains).

  Two disruptive cases (`580`, `600`) stay in-tree
  behind `SLINIT_ALLOW_DISRUPTIVE=1` — they exist to validate
  the DirLoader fix and would kernel-panic slinit again if
  the mutex is ever removed. One case (`810`
  socket-activation-on-demand) is a documented SKIP pending
  socket-activation semantics review.

  Publishable findings (numbers on ceres real hardware, kernel
  7.2.3-lowlatency-sunlight1, Go 1.26.8):

  - Slinit is CLI-fork/exec-bound, not IPC-bound: baseline
    slinitctl call = 1.06 ms; every socket op is within
    20-700 μs of the baseline.
  - Concurrent client scaling is FLAT to 128 clients (0.165
    ms/client at 128, matching 0.177 ms/client at 32) — no
    mutex contention.
  - Journal filter push-down implemented correctly: `-t tag`
    is 26% FASTER than unfiltered, `-p err` is 50% faster.
  - Bulk ops dramatically cheaper than N × individual: `reload-
    all` = 1.30 ms (10x cheaper than 13 sequential reloads);
    `restart` (1.29 ms) = HALF of `start && stop` (2.69 ms).
  - Fair scheduling verified: light `status` p99 under
    sustained heavy `-n 5000` reads = 1.94 ms, essentially
    identical to isolated baseline (1.98 ms).
  - Dep-walk on 100-deep chain = 1.94 ms (~8 μs/node) — linear
    but tiny.
  - Path-activation e2e (touch → observed STARTED) = 1.305 ms —
    inotify essentially real-time.
  - Full svc lifecycle (write file → start → stop → unload → rm)
    = 3.89 ms serial; 4.90 ms at 4-way concurrency (post-fix).

- **`tests/performance/ssh/run.sh` forwards
  `SLINIT_ALLOW_DISRUPTIVE`** through the SSH invocation so
  disruptive-gated cases run when the operator explicitly
  opts in. Previously only `ITERS` was passed to the remote
  shell — disruptive cases always saw the env var unset and
  always skipped.

## [2.2.6] — 2026-09-05

Journal-recovery hardening pass + enable/disable workflow polish +
first real cold-boot performance harnesses. The journal fixes are
motivated by the same class of crash-tolerance bug: a hard reboot
mid-write leaves the tail of a `.journal` file in one of several
truncated shapes, and prior code panicked or crash-looped on each.
This release absorbs every truncation shape observed while shaking
down the demo's `--persist` flag. The demo/UX pass closes the last
gap where `slinitctl enable X` / `disable X` didn't round-trip
symmetrically against `all-services.d/`. The perf-benchmark harnesses
give slinit its first published cold-boot numbers so it can be
plotted alongside dinit / systemd / runit in comparison literature.

### Added

- **`tests/performance/demo/` — four QEMU cold-boot harnesses.**
  `cold-boot.sh` boots the full 34-service demo, `minimal-boot.sh`
  strips to a single service for parity comparison, `fork-exec-
  throughput.sh` generates N `type=scripted, command=/bin/true`
  mock services to isolate per-service fork+exec+wait cost, and
  `pid1-footprint.sh` measures steady-state RSS after an idle
  wait (Go GC settle). All four share `_lib.sh`: a
  `perf_write_collector` that emits the measurement service
  inline (parameterised depends-on + optional pre-dump sleep),
  `perf_extract_block` that strips ANSI colour + the `\r`
  `/dev/console` tacks on, and `perf_median` / `perf_p95` /
  `perf_summary` for the benchstat-compatible output tables.
  Each harness owns only its own service-tree wiring; driver
  loop is identical.
- **`tests/performance/` tiered into `runtime/`, `demo/`, `ssh/`.**
  Existing Go micro-benchmarks moved to `runtime/` (package
  renamed to `perfruntime` since Go's stdlib owns `runtime`).
  `demo/` holds the QEMU-driven harnesses above; `ssh/` reserved
  for planned SSH-driven end-to-end latency benchmarks
  (`ctl-latency`, `journalctl-throughput`, `enable-disable`).
  README at each tier explains what belongs where.

### Fixed

- **`pkg/journalbin` writer: chain recovery tolerates truncated
  ENTRY_ARRAY tail.** Prior `recoverEntryArrayTailLocked`
  returned EOF as-is when the last written entry-array header
  extended past end-of-file, which crash-looped the demo's
  `journal-demo` service after every unclean shutdown. Now
  treats truncation as "scan boundary reached, chain valid up
  to here" — the reader clamps at the last complete entry-array
  and future writes append cleanly.
- **`pkg/journalbin` writer: FSS recovery tolerates truncated
  tail.** `recoverFSSStateLocked` had the same class of bug in
  the sealed-tag scan path. Extended `isTruncationErr` to
  recognise `io.EOF`, `io.ErrUnexpectedEOF`, and their
  `fmt.Errorf %w` wraps as end-of-usable-data signals rather
  than fatal errors.
- **`pkg/journalbin`: "expected ENTRY_ARRAY got X" treated as
  truncation.** When the tail region has been zero-filled by a
  crash mid-fallocate, the object type at the recovery cursor
  reads as UNUSED (or any other non-ENTRY_ARRAY value) instead
  of returning EOF. Previously fatal; now treated as
  "recovered as much as possible, stop scanning".
- **`pkg/journalbin`: FSS recovery treats `size < HeaderSize` as
  scan boundary.** A partially-written object header with a
  size field smaller than the header size itself is another
  truncation shape — same fix applies.
- **`pkg/journalbin` Iter: overflow-safe bounds + forward-
  progress guard.** Fuzzing surfaced an integer-overflow path
  where `Iter` would loop indefinitely on a hostile file
  (`ObjectHeader.Size` wraparound + `AlignUp` overflow). Bounds
  now use overflow-safe arithmetic, and a forward-progress
  guard aborts if the cursor doesn't advance between iterations.
- **`cmd/slinit-journalctl`: `isTruncationErr` classifies zero-
  region shapes too.** `--list-boots` walk hit the same trap as
  the writer: reading past the end of a truncated file returned
  errors that weren't in the original `isTruncationErr`
  predicate, aborting the multi-file scan mid-walk. Extended
  the classifier to match the writer's set.
- **`demo/services/all-services` uses `waits-for.d =
  all-services.d/`.** Previously listed every workload with an
  explicit `waits-for:` line, so `slinitctl enable X` /
  `disable X` couldn't manipulate the boot bundle without
  editing the file. Now enumerates via a symlink directory
  operators toggle — the OpenRC `/etc/rc.conf`-alike UX slinit
  was missing.
- **`waits-for.d` directive takes `:` operator, not `=`.**
  Config parser accepts both forms for value directives but
  dependency directives (including `waits-for.d`) require `:`.
  Fixed in the demo service file that hit this first.
- **`pkg/control` persistEnable/Disable respect the service's
  `waits-for.d` directive.** `slinitctl enable X` / `disable X`
  had hardcoded `waits-for.d/` as the symlink target directory,
  so services declaring a non-default `waits-for.d` name (like
  `all-services.d`) wouldn't round-trip. Now looks up the
  actual directory from the service's config via a new
  `WaitsForDirs()` accessor and `enableDirFor()` helper.
- **`demo/services/*` @meta enable-via all-services.** Adds
  `@meta enable-via all-services` to every workload that
  should be togglable via `slinitctl enable/disable`, so the
  CLI resolves the right from-service instead of failing on
  ambiguity. 34 services touched.

## [2.2.5] — 2026-09-04

Journal multi-boot pass. `journalctl --list-boots` now surfaces
every boot the on-disk journal covers (not just the current
process's ring buffer), `-b -N` relative indexing resolves via
the same aggregator, and the QEMU demo gained a `--persist` flag
that wires a virtio disk through to `/var/log/slinit-journal` so
multiple hard-reboot cycles accumulate in `--list-boots` instead
of getting wiped by the initramfs tmpfs. Two PID-1 SIGCHLD reaper
race fixes shipped alongside — noticed while shaking down the
demo, applied under the "surfaces once, fix once" principle.

### Added

- **`journalctl --list-boots` walks on-disk journals**. Prior
  implementation queried only the control-socket ring buffer, so
  a host with a running `slinit-journald` still only saw the
  current boot. `runListBoots` now aggregates BootID→(first_ts,
  last_ts) from three sources: the ring buffer, every `.journal`
  binary Phase B file under `/var/log/slinit-journal/` +
  `/run/slinit-journal/` (via `pkg/journalbin.MultiReader.Iter`),
  and every `.jsonl` / `.jsonl.gz` Phase C file in the same
  dirs. Same-BootID entries across sources collapse into one row
  spanning the widest bounds seen.
- **`journalctl -b -N` relative boot indexing**. `-b -1` for
  "previous boot" was the workhorse of post-mortem debugging on
  systemd hosts and slinit previously rejected it with a
  helpful-but-noisy "use --list-boots to look up the ID". New
  `resolveBootSpec` walks the same aggregator and resolves `-N`
  to a concrete 32-hex boot-id, then routes the query through
  `runFromDirectory` (which reads `/var/log/slinit-journal/`)
  with a new `QueryFilter.BootID` field so events from other
  boots don't leak through. `-b <hex>` and `+N` shapes handled
  in the same code path. `-b -N -f` rejected — past boots don't
  emit new events.
- **`journalctl --list-boots` tolerates unclean-shutdown
  truncation**. A hard reboot mid-write leaves the tail
  ENTRY_ARRAY of the active `.journal` file pointing past
  end-of-file. New `isTruncationErr` classifier absorbs `io.EOF`
  / `io.ErrUnexpectedEOF` (and their `fmt.Errorf %w` wraps) as
  "reached end of usable data on this file, keep going" while
  still surfacing genuine unreadable-file errors (bad magic,
  permissions). Investigating "why did we hard-reboot last time"
  now returns useful results instead of erroring out on the
  corrupt-tail file.
- **`journalctl` accepts `.journal` files under `--directory`**.
  `journalFilesUnder` had `.jsonl / .jsonl.gz / .slj` but was
  missing `.journal` (binary Phase B extension), so
  `runFromDirectory` couldn't find binary files even though
  `--list-boots` could. Whitelist extended.
- **`demo/run.sh --persist` provisions a persistent virtio
  disk**. Creates `_output/journal.img` (256 MB sparse) on first
  use, attaches via `-drive file=…,format=raw,if=virtio`, and
  echoes the resulting flag on the startup line so the operator
  can confirm QEMU received it. Kernel modules (`virtio_blk`,
  `virtio_pci`, `fat`, `vfat`, `nls_cp437`, `nls_ascii`) now
  bundled into the initramfs from the Alpine linux-virt package.
- **`demo/services/persist-journal-mount`**: `scripted` service
  that mkfs-vfats `/dev/vda` on first use and mounts it at
  `/var/log/slinit-journal` with explicit `codepage=437,
  iocharset=ascii` (vfat mount rejects `EINVAL` without a loaded
  NLS charset). Idempotent — a `mountpoint -q` gate skips the
  setup on softreboot passthrough, and a `mount -o remount,rw`
  handles the softreboot "shutdown left it ro" case. Logs its
  mount-vs-tmpfs decision to `/run/persist-journal-mount.log`
  for post-boot inspection. Depends on `system-init`;
  `journal-demo` depends on it.
- **`demo/build.sh` caches the FSS key at `_cache/journal-key`**
  so it survives `rm -rf _build/rootfs` on every rebuild.
  Previously each `./build.sh` minted a fresh random key,
  invalidating the TAG chain in any journal file written by a
  previous `--persist` run — the next boot's `slinit-journald`
  crash-looped with exit 1. Delete `_cache/journal-key`
  intentionally to rotate.
- **`demo/README.md`**: new "Multi-boot journal listing" section
  documenting the `--persist` workflow, softreboot-preserves-
  boot-id caveat (systemd parity — must hard-reboot for a new
  entry), and reset instructions.

### Fixed

- **`waitid ECHILD` swallow in cron + finish-command**. slinit as
  PID 1 runs a global SIGCHLD reaper (`pkg/process/exitrouter.go`)
  that collects every child zombie. `CronRunner.executeCommand`
  and `ProcessService.execFinishCommand` both use vanilla
  `os/exec.CommandContext` — they compete with the reaper, and
  when the reaper wins `cmd.Run()` returns `waitid: no child
  processes`. The commands DID run and exit; only the exit code
  is unobservable. Absorb `ECHILD` via new `isECHILDErr` helper
  in `pkg/service/cron.go`; `finish-command` at
  `pkg/service/process.go:2628` reuses the same predicate.
  Regression-guarded so `*exec.ExitError` still surfaces
  correctly.

### Housekeeping

- `demo/services/*`: escaped bare `$VAR` / `$((…))` / `$(cmd)`
  as `$$` in 20 service files (`hello`, `ticker`, `app-one`,
  `app-two`, `cron-demo`, `hello-logged`, `vtty-svc`,
  `backoff-demo`, `logfile-demo`, `logger`, `shared-log`,
  `sandbox-demo`, `cgroup-demo`, `cgroup-worker`, `ssd-demo`,
  `extra-actions`, `healthcheck-demo`, `env-demo`, `socket-demo`,
  `runit-svc`). Slinit's `command =` parser pre-expands `$VAR`
  at load time (documented behaviour), so shell-side references
  need `$$` to reach the runtime shell. Fixed the visible
  "message  " (empty counter) noise the demo produced.

## [2.2.4] — 2026-09-03

Feature-heavy release: full `slinit-journalctl` systemd parity (flag
surface + short-form aliases + output formats), first-class nspawn-
style container integration (three-tier architecture + working demo),
and a correctness fix for a state-machine race that Tier 3 fuzz
surfaced. Every code fix that landed here originated in a test that
now guards it.

### Added

- **`pkg/machine` — flat-file container registry** (Tier 1). Files
  under `/run/slinit/machines/<name>` map operator-chosen names to
  the container's host-visible PID 1. No D-Bus, no management
  daemon — mirrors OpenRC/runit "flat files instead of a service
  manager for the service manager". 14 tests cover roundtrip, atomic
  overwrite, invalid-name/path-traversal/bogus-PID rejection,
  liveness probing, and journal-file enumeration under a container
  rootfs.
- **`slinit-machinectl`** (Tier 2): CLI over `pkg/machine` with
  `register / unregister / list / status / show` subcommands.
  Modelled on systemd's `machinectl` subset that fits slinit's
  runtime model.
- **`slinit-nspawn`** (Tier 3): namespaced container launcher.
  Creates PID / mount / UTS / IPC (+ optional NET) namespaces via
  self-reexec + `CLONE_NEW*`, pivot_root into the target rootfs,
  standard virtual FS bind mounts (`/proc`, `/sys`, `/dev/pts`,
  `/run`, `/tmp`), then execs `/sbin/slinit` (or `--init=PATH`) as
  PID 1 inside. Signal proxy forwards SIGTERM/SIGINT from the
  launcher to container PID 1; registry cleanup runs on child exit.
- **`slinit-journalctl -M CONTAINER`** now dispatches into the
  container's journal via `pkg/machine` + `/proc/PID/root/run/
  slinit.socket`, replacing the earlier WARN-shim. Explicit `--file/
  --directory/--root/--image` still win. Unknown or stale machines
  return a clean error identifying the registry path.
- **`slinit-journalctl` full 15/15 output-format parity** with
  systemd. 9 new modes: `short-precise`, `short-iso-precise`,
  `short-full`, `short-monotonic` (dmesg-style `[    S.uuuuuu]`
  bracket), `short-unix`, `with-unit` (multi-unit disambiguation),
  `json-pretty` (indented), `json-sse` (`data: JSON\n\n` for browser
  EventSource), `json-seq` (RFC 7464 `\x1e JSON \n` for `jq --seq`).
- **`slinit-journalctl` full 65/65 flag-surface parity** with
  systemd `journalctl`. Short-form aliases `-S/--since`, `-i/--file`,
  `-N/--fields`, `-W/--no-hostname`. New `-I` resolves to the latest
  invocation of `-u UNIT` at run time (requires `-u`, mutually
  exclusive with `--invocation=`). `--no-pager` accepted as a silent
  no-op for script portability.
- **`condition-cpu-feature` arch-prefix syntax** (`arm64.bti` ⇔
  bare `bti` on arm64, false everywhere else) — retroactive port of
  systemd `d518675741` (2026-07-10) missed in the July 14 v261 sweep.
- **`demo/alpine-nspawn/`**: end-to-end walkthrough with Alpine
  minirootfs, three sample services (ticker/httpd/shell all pure
  `/bin/sh` loops so no busybox-applet dependencies), fetch + setup
  + run + stop scripts, and README documenting the flow. Live-
  tested — `-M alpine-demo -f` streams container stdout in real time.

### Fixed

- **wedged-STOPPING state race** (surfaced by Tier 3 fuzz seed
  `[88 48 44 49]`). `svc.Record().PinStart()` enqueued
  `propPinDpt=true` but relied on the caller to drain — a racing
  `Stop(dep)` in the window between PinStart and ProcessQueues
  observed stale `deptPinnedStarted=false` and ran `doStop`,
  leaving the dep wedged in StateStopping forever. Fixed with new
  `ServiceSet.PinStartService` / `UnpinService` wrappers that hold
  `queueMu` + call `processQueuesLocked` atomically, mirroring
  dinit's control-layer `pin_start(); process_queues();`
  convention. All three production callers routed through the
  wrappers. `TestPinStartServiceWrapperDrainsPropagation` locks
  in the fix.
- **`readEntryArray` unbounded allocation** (fuzz-caught DoS): a
  hostile `ObjectHeader.Size` in a `.journal` file triggered a
  multi-EB `make([]byte, oh.Size-16)` and OOMed the reader. New
  `maxEntryArrayObjectSize` cap tied to the writer's
  `entryArrayMaxCap=4096`. Corpus `0e62be2bf96a4a88` guards the
  regression.
- **`ParseCPUAffinity` unbounded loop** (fuzz-caught DoS): input
  `0-79999999` allocated ~640 MB of `[]uint` + a same-size map.
  New `MaxCPUNumber=65535` cap (10× above any realistic CPU count)
  rejects with a clean error. Corpus `fdbd62cae53e1173` guards.
- **Wire-layer NUL rejection in `DecodeServiceName`** (fuzz-caught
  defense-in-depth) — see `pkg/control`.
- **`readZoneTab` filters NUL + `..` + `/`-prefixed entries**
  (fuzz-caught defense-in-depth) in `slinit-timedatectl` zoneinfo
  enumeration.
- **`115-protect-hostname` / `113-protect-clock` / `112-protect-
  kernel-logs` / `111-protect-kernel-modules`** — polled
  `/proc/PID/status` for Seccomp mode over 10×200 ms rather than
  a single-shot read after `sleep 0.5`. Same fix pattern as
  `116-lock-personality` shipped earlier; race is between slinit's
  post-fork `Started()` event and `slinit-runner`'s pre-exec
  `seccomp.Install()`.

### Tests

- **Fuzz suite: 21 targets → 34 targets** across 9 packages,
  in three focused passes:
  - **Tier 1 (depth)**: round-trip invariants on 8 control-protocol
    encode/decode pairs + `FuzzServiceNameSemantics` wire-vs-loader
    consistency check that immediately surfaced the NUL-in-
    DecodeServiceName gap.
  - **Tier 2 (breadth)**: 12 new targets across 8 packages
    (`FuzzFstabParse`, `FuzzJournalBinaryDecodeHeader`,
    `FuzzJournalBinaryOpenReader`, `FuzzDecodeValue`,
    `FuzzLoadMachineInfo`, `FuzzParseOSRelease`, `FuzzReadZoneTab`,
    `FuzzValidateZone`, `FuzzTmpfilesParseLine`,
    `FuzzSysusersParseLine`, `FuzzParseSystemdUnit`,
    `FuzzAnalyzeRunScript`, `FuzzParseChpst`,
    `FuzzParseOpenrcScript`, `FuzzParseDepend`).
  - **Tier 3 (state machine, in-package `pkg/service`)**: single
    `FuzzStateMachine` driving a 3-node dep chain through pseudo-
    random sequences of Start/Stop/Restart/ForceStop/PinStart/
    Unpin/ProcessQueues; surfaced the wedged-STOPPING bug fixed
    above.
- **Full-suite verified on 40-core hardware**: `for f in $(go test
  -list 'Fuzz.*'); do go test -fuzz=$f -fuzztime=5m; done` runs
  ~1–2 billion iterations across all 34 targets with zero new
  findings after this session's fixes.
- **Alpine-nspawn end-to-end** live-tested: `sudo ./run.sh` boots
  the container, `slinit-journalctl -M alpine-demo -u ticker -f`
  streams `TICK N` lines at 5 s cadence with correct hostname
  (`alpine-demo`) and container-local PID (12).

### Housekeeping

- `.gitignore` refactored to glob patterns (`/slinit-*`, `/rc-*`,
  `/slinitctl-*`) — every existing + future `cmd/` binary covered
  without another edit. `git check-ignore` verified `cmd/` source
  directories remain fully tracked.
- `demo/build.sh` now bundles `slinit-machinectl` + `slinit-nspawn`
  into the QEMU rootfs.
- `tests/fuzz/README.md` sequential-runner snippet anchors `-fuzz`
  regex with `^$` so a name that's a prefix of another target
  (`FuzzDecodeServiceStatus` vs `FuzzDecodeServiceStatus5`)
  doesn't fail with "matches more than one".

## [2.2.3] — 2026-08-31

Test-only point release on top of v2.2.2. No behaviour changes to
slinit itself.

### Test infrastructure

- **`116-lock-personality` — polls for seccomp filter install.**
  Under slow VMs the single-shot `/proc/PID/status` read in this
  test could catch the child between `slinit`'s post-fork
  `Started()` call and `slinit-runner`'s pre-exec `seccomp.Install()`
  — `Seccomp:` briefly reads 0, the test asserts 2, flake. Poll the
  field for up to 2s (10 × 200ms) so the runner has room to finish
  its hardening pipeline on any reasonable host; the fast-path
  break-out keeps normal runs at zero extra latency. Slinit itself
  is behaviourally correct: `STARTED == fork happened`, matching
  systemd `Type=simple` semantics.

## [2.2.2] — 2026-08-30

Point release on top of v2.2.1 — three state-machine bug fixes
targeting long-latent regressions in the restart / smooth-recovery
paths, plus one small dinit-parity port and a DMI detection
addition.

### Code fixing

- **`restart-delay` + `restart-delay-step` / `restart-delay-cap` now
  applied on non-smooth-recovery restarts.** Latent since the
  progressive-backoff feature landed. `nextRestartDelay()` was
  called only from `doSmoothRecovery`; services configured with
  plain `restart = on-failure` or `restart = yes` respawned as fast
  as the state machine could turn over, ignoring the entire delay
  machinery. A crash-looping service would burn every restart-limit
  slot in milliseconds before the backoff had a chance to slow it
  down. Fix routes `Stopped()`'s willRestart branch through a new
  `Service.ScheduleRestartWithBackoff()` hook — `ServiceRecord`'s
  default returns false (no delay) so unaffected service types
  keep their existing behaviour; `ProcessService` overrides to
  compute `nextRestartDelay()`, and if `time.Since(lastStartTime) <
  effectiveDelay`, arms a `time.AfterFunc` for the remainder and
  defers `initiateStart` until the timer fires. Uses a private
  `AfterFunc` timer rather than the shared `processTimer` because
  `monitorProcess` exits after `handleChildExit` returns, leaving
  the shared timer's monitor-loop listener dead. The callback
  re-checks `state == StateStopped && desired == StateStarted`
  before firing so an operator who runs `slinitctl stop` while the
  timer is pending doesn't get a surprise restart. Surfaced by
  functional test `53-restart-backoff` which had been failing
  silently (gap1=1s from `date +%s` second-boundary noise,
  gap2=0s).

- **Smooth-recovery readiness-pipe failure treated as termination.**
  Mirrors dinit upstream `991fceeb` (2026-08-30, "Better handling of
  smooth recovery failure"). A service with `notify` +
  `smooth-recovery` whose respawned process closed the readiness
  pipe without signalling (child crashes pre-notify, `dup2` moves
  fd 3 elsewhere) was silently ignored by
  `handleReadyNotification`'s early-return on `state !=
  StateStarting`, leaving the service wedged at `StateStarted`
  with no live PID while dependents believed it was up. Fix
  extends the state-check branch to detect `state == StateStarted
  + ready == false` and route through
  `handleUnexpectedTerminationLocked` so the state machine
  transitions out. Narrow combination (`notify` + smooth-recovery
  + child that crashes post-fork pre-notify), but silent wedging
  is the worst failure shape when it hits.

- **`slinit-hostnamectl` detects Alibaba Cloud ECS as KVM.** Mirrors
  systemd `abffa868a8`. The narrower `Alibaba Cloud ECS` match
  (rather than the bare `Alibaba Cloud` sys_vendor that can appear
  on non-VM hardware) tags actual ECS instances instead of falling
  through to the cpuinfo hypervisor-flag `unknown`. Coverage-locked
  by `TestDetectVM_DMIMatches` across eight vendors.

### Test infrastructure

- **`TestScriptedServiceNoStrayTimers`** — defense-in-depth
  regression lock for the class of bug dinit closed with
  `6ee41c74` (missed timer clear on scripted-service exit paths).
  Slinit is structurally safe (`cancelTimer()` unconditional at the
  top of every `handleStartExit` / `handleStopExit`), but this
  test guarantees a future refactor can't quietly move the cancel
  into a conditional branch without the CI catching it. Five
  subtests cover the representative exit paths (successful
  start+stop, start-command failure, exec failure, stop-command
  failure, no-commands immediate transitions).

## [2.2.1] — 2026-08-12

Point release on top of v2.2.0 — two new systemd-compat CLIs land as
first-class binaries, and the boot-failure rescue shell becomes
usable on serial consoles.

### New features

- **`slinit-hostnamectl`** — systemd `hostnamectl(1)` parity,
  D-Bus-free. Native Go implementation of `status` / `hostname [NAME]`
  / `icon-name` / `chassis` / `deployment` / `location`, with the
  scope flags `--transient` / `--static` / `--pretty` and JSON output
  (`--json=pretty|short|off`, `-j`). Reads and writes on-disk sources
  directly: `/etc/hostname` (kernel + static), `/etc/machine-info`
  (pretty / icon / chassis / deployment / location + hardware
  metadata), `/etc/machine-id`, `/proc/sys/kernel/random/boot_id`,
  `/etc/os-release`, `/sys/class/dmi/id/*`, `uname(2)`. Chassis
  auto-detects from SMBIOS chassis_type + container / VM heuristics
  (docker, podman, lxc, nspawn, kvm, qemu, vmware, virtualbox, xen,
  hyper-v, ec2). `-H/--host` and `-M/--machine` parse cleanly but
  return an error at runtime — no D-Bus, no nspawn integration.
  `hostnamectl` symlink onto `slinit-hostnamectl` for muscle memory.

- **`slinit-timedatectl`** — systemd `timedatectl(1)` parity,
  D-Bus-free. `status` / `show` (KEY=VALUE) / `set-time` (RFC3339,
  systemd form, `@epoch`, relative `+5min`/`-2h`/`+1d`, `now`) /
  `set-timezone` (atomic `/etc/localtime` symlink swap + zone
  validation via TZif magic, rejects `..` and absolute paths) /
  `list-timezones` (prefers `zone1970.tab`, falls back to `zone.tab`,
  then filesystem walk) / `set-local-rtc [--adjust-system-clock]`
  (writes `/etc/adjtime` line 3, optional `hwclock --systohc`
  passthrough) / `set-ntp` (probes `/etc/slinit.d/` for chronyd /
  systemd-timesyncd / ntpd / openntpd / sntp, then `slinitctl
  enable/disable` + `start/stop`). Reads `/dev/rtc` via
  `RTC_RD_TIME` and interprets it per `/etc/adjtime`. `-H/--host` /
  `-M/--machine` + timesyncd-specific subcommands
  (`timesync-status` / `show-timesync` / `ntp-servers` / `revert`)
  parse but return errors at runtime with pointed diagnostics.
  `timedatectl` symlink for muscle memory.

### Code fixing

- **Rescue shell usable on serial consoles.** The `[s]` drop-to-shell
  action after a boot failure produced a shell where the operator's
  input was half-eaten (typing `stty` yielded `ty`), empty
  `bash-5.3#` prompts multiplied, and busybox ash flooded the
  console with `[38;5R` cursor-position report replies. Five small
  patches close every failure mode: the boot debugger's console
  reader is stopped before the rescue menu opens (was racing with
  the shell for every byte); `TIOCSWINSZ` seeds a 24×80 winsize on
  serial ttys with no WINCH history (stops the line-editor fallback
  that queries cursor pos via `ESC[6n`); the tty input buffer is
  flushed at exec (drops stale terminal replies from the menu's
  rendering); `TERM=dumb` + `LINES=24` + `COLUMNS=80` are exported
  to the child so bash's readline skips ANSI entirely; and
  `/bin/bash` sits before `/bin/sh` in shell candidates (bash
  respects `TERM=dumb`, busybox ash doesn't for the query). Applied
  at every fork-shell site — rescue menu, boot debugger `[s]`,
  `--debug-shell` on tty9, `bootmode=rescue/emergency`. `demo/run.sh`
  gains a `--no-monitor` flag that swaps `-serial mon:stdio` for
  `-serial stdio -monitor none` — was a diagnostic detour, kept as
  a useful knob for similar console investigations.

- **Transient-hostname sentinel filter.** `hostnamectl status`
  hid the `Transient hostname` field when the kernel reports one of
  the "unset" sentinels — `(none)` (Linux placeholder before any
  `sethostname(2)` call) or `localhost` / `localhost.localdomain`
  (distro-default before real config lands). Matches systemd-hostnamed's
  same filter and stops the field from showing meaningless text on
  fresh installs.

- **Decoded wait status in stop-command failure log.** The rare
  "stop command failed" error line printed the raw 16-bit wait(2)
  status word (`status: 1280`) instead of the decoded exit code
  or signal. Now emits `exit code N` for normal exits, `killed by
  signal N (name)` for signal deaths, or a labeled raw form for
  the stopped/continued edge case.

- **`restart-limit-count = 0` now honored as "unlimited".** The
  runtime already treated `maxRestartCount == 0` as "no restart
  rate limit" (matching what an operator writing 0 in a service
  file expects), but the loader was silently dropping explicit
  zeros: the four `if desc.RestartInterval > 0 ||
  desc.RestartLimitCount > 0` gates couldn't distinguish an
  unset int field from an explicit zero, so the built-in default
  (3 restarts / 10s) kept applying. Added a `RestartLimitCountSet
  bool` companion field so the parser can record intent; loader
  gates now key off it. Negative values are rejected at parse
  time. The demo's `tty` service now uses this (`command =
  /bin/bash --login -o ignoreeof` + `restart-limit-count = 0`)
  so an operator can `logout` or Ctrl-D as many times as they
  like without cascading the boot into a BOOT COLLAPSE — matches
  what `agetty` on `tty1..N` does.

## [2.2.0] — 2026-08-09

**Stable-release milestone.** Rolls up the entire v2.1.x line
(v2.1.0 → v2.1.14) into a minor-version bump so downstream
packagers, distros, and image builders can pin against a single
stable tag before the v2.2.x lane opens next week. No code
changes on top of v2.1.14 — the git-tree contents are
byte-identical to `v2.1.14`; the tag exists purely as a stable
anchor.

### What v2.1.x delivered (recap)

- **Journal pipeline — 65/65 systemd `journalctl` flag parity**
  (v2.1.0 → v2.1.12). Full query surface (`-t/-T/-g` with case
  heuristic, `-b/--boot/--this-boot`, `-c/--cursor/--after-cursor/
  --cursor-file`, `--since/--until` with human-time forms),
  display (`--utc/--no-hostname/--truncate-newline/--no-full/
  --output-fields`, 6 `-o` formats), introspection (`--fields/
  --header/--disk-usage/-F/--list-boots`), maintenance
  (`--sync/--rotate/--vacuum-*` + `--flush/--relinquish-var/
  --smart-relinquish-var` via UNIX DGRAM admin socket), FSS
  sealing (`--setup-keys/--force/--verify/--verify-key/
  --interval`), message catalog (`-x/--dump/--list/--update`),
  invocation tracking (per-start `SLINIT_INVOCATION_ID` +
  `--invocation` + `--list-invocations`), journal namespaces
  (per-daemon `--namespace=NS` with auto-suffixed paths +
  filter + `--list-namespaces`), disk-image dissection
  (`--image` + `--image-policy` via losetup+mount). Backing
  daemon writes both JSONL (Phase C, gzip-rotated) and binary
  (Phase B, SLJRNL01 with FSS TAG chain via HKDF-SHA256 +
  HMAC-SHA256).
- **Migration converters** (v2.1.4-5): scripted migration path
  from runit, OpenRC, and systemd via `slinit-runit-convert` /
  `slinit-openrc-convert` / `slinit-systemd-convert`. Runit
  converter validated 46/46 lint-clean against real void
  `/etc/sv/*` including `log/run` companion pairing with
  `log-type = pipe` + `consumer-of`; auto-emits `waits-for:
  DEP` from `sv check DEP` in run scripts; recognises the
  runit 2025-08 `chpst -A` alarm flag (WARN, no slinit
  runtime-alarm primitive).
- **`slinit-supports`** self-introspection CLI (v2.1.0) —
  `--list-directives` / `--list-opcodes` / `--list-all` and
  name lookup so package managers can query slinit's
  capability set without parsing source.
- **`slinitctl analyze`** subcommand dispatcher (v2.1.2) —
  `time`, `blame`, `critical-chain`, `dot`, plus a `plot`
  stub. Replaces the removed `slinit-analyze` binary.
- **Recovery + boot-debugger subsystem (`pkg/recovery` +
  `pkg/bootmode`)** — the largest v2.1.x land after journalctl.
  Two new packages plus ~22 recovery commits across v2.1.1 →
  v2.1.2 bring slinit to systemd-analyze parity on the boot-
  failure UX axis.
    - **v2.1.1 — Interactive boot debugger** (`pkg/recovery`):
      Ctrl-B trigger during boot opens a rescue menu (cbreak
      tty mode so keypresses fire without Enter, tcflush of
      pending tty input before menu reads, EOF from canonical-
      mode maps to `ActionRetry` for Ctrl-D UX). Force-fail
      target filters aggregate services (they can't be force-
      failed meaningfully). Boot debugger detaches from the
      console BEFORE a console-owning service exec so it never
      clobbers the child's terminal.
    - **v2.1.2 Phase 1 — Structured kernel-cmdline parser**
      (`pkg/bootmode`): typed `Options` struct with `Mode`
      enum (Default/Emergency/Rescue), plus `DebugShell`,
      `ConfirmSpawn`, `CrashShell`, `LogLevel`, `Debug`.
      `Parse(string)` and `ParseFromProc()`. Wires
      `slinit.log-level=` straight into `logger.SetLevel`.
    - **v2.1.2 Phase 2+3 — Emergency vs Rescue split + tty9
      debug-shell**: Emergency drops to `sulogin` before
      services start; Rescue keeps the control socket +
      eventloop alive so operators can `slinitctl` the box
      while debugging; tty9 debug-shell runs in a respawn
      loop when `slinit.debug-shell` is on the cmdline.
    - **v2.1.2 Phase 4 — `slinit-analyze` replaced by
      `slinitctl analyze`** subcommand dispatcher: `time /
      blame / critical-chain / dot / plot`; the standalone
      `slinit-analyze` binary was removed as a duplicate.
    - **v2.1.2 Phase 5 — Confirm-spawn + crash-shell**
      (systemd parity): `confirm-spawn` gates every service at
      `allDepsStarted` so all 5 service types prompt (process /
      scripted / bgprocess / internal / triggered), single-
      keypress cbreak dispatch. `crash-shell` drops into a
      shell on PID 1 goroutine panic, with `SetCrashPause`
      freezing every subsequent `callBringUp` while the shell
      runs so a bounce-loop doesn't kill the debugging session.
      `defer shutdown.CrashRecovery` wraps every long-lived
      goroutine (rescue/debug-shell/test-hook) since Go's
      main-goroutine `defer recover` doesn't catch other-
      goroutine panics.
    - **v2.1.2 Phase 6 — Recovery-pkg cleanup + unified UX**:
      shared menu-box primitives (`menuBoxBar`,
      `writeBoxHeader/Blank/Line/Footer`); `Debugger.Stop`
      waits on `menuMu` before restoring termios so a live
      menu doesn't get its state pulled out from under it;
      `readByteWithTimeout` uses `time.After` per iteration +
      `clearPrompt` for a clean redraw. `pkg/logging` gains
      `PauseBootConsole`/`ResumeBootConsole` (via
      `bootConsolePaused atomic.Bool`) so the boot banner
      doesn't inter-print with a live menu.
    - Follow-up UX polish: five menu fixes (a56be20), end-to-
      end crash-shell validation + service-freeze during
      drop (24c4f20), signal-driven exit-noise swallowed on
      `runRescueShell` (929830f), demo fstab uses `noauto` so
      Rescue's `mount -a` exits clean (5ae8802).
- **Dinit-parity sweep** (v2.1.0): `DINIT_SERVICE` /
  `DINIT_CS_FD` / `DINIT_SOCKET_PATH` env-var aliases,
  `/etc/slinit/environment` auto-load, `XDG_CONFIG_HOME` +
  `$HOME/.config` dedup, dual-wire disable (`CmdDisableServiceV7=62`
  atomic default; `--dinit-compat` routes through
  `CmdRmDepV7=30` for real-dinit interop).
- **Test-suite catch-up** (v2.1.13): 39 new cases total (22
  acceptance, 17 functional) closing runtime-testable coverage
  for the v2.1.x arc.
- **Dev tooling** (v2.1.14): `tools/stats/` binary walks the
  repo for LOC / test / structure / feature-surface / doc
  counts (text / `--json` / `--markdown`). Excluded from the
  slpkgs template — dev-only.

### Verified surface

- **35 binaries** under `cmd/`
- **29 packages** under `pkg/`
- **2,413 test cases** total: 1,956 unit + 21 fuzz + 218
  functional (QEMU) + 218 acceptance (SSH); all green on the
  v2.1.14 CI run (`go vet` clean, race-detector clean, cross-
  compile clean for every command).
- **317 config directives + 106 wire opcodes** discoverable
  via `slinit-supports --list-directives` / `--list-opcodes`.

### Next lane

The v2.2.x line opens for the next batch of features / fixes.
Per per-version CHANGELOG detail below, this cut is the anchor
for downstream packagers that want a stable tag between the
v2.1.x rapid-iteration lane and whatever v2.2.x brings.

## [2.1.14] — 2026-08-08

Small triage cut: one upstream-parity fix in the runit converter,
one dev-only stats binary, and a big documentation refresh so the
README + CONTRIBUTING + man page + test-suite READMEs all match
the v2.1.0 → v2.1.12 surface. No changes to runtime behaviour.

### Fixed

- `slinit-runit-convert`: recognise runit 2025-08's new
  `chpst -A seconds` flag (SIGALRM timer, upstream commit
  `45b7fde`). Before this, `chpst -A 30 daemon` was silently
  parsed as an unknown flag + `command = "30 daemon"`. The
  converter now consumes the `-A` value + emits a WARN naming
  the missing slinit primitive (there's no runtime-alarm
  equivalent; `stop-timeout` is documented as the closest but
  different-semantic alternative). Regression guard:
  `TestParseChpstAlarmDoesNotEatCommand`. Landed as `bc986f0`.

### Added

- `tools/stats/` — dev-only project statistics binary. Walks
  the repo and reports LOC by language (Go / Shell / Markdown /
  YAML / XML / JSON / Makefile with code/comment/blank split),
  test counts (unit / fuzz / functional / acceptance — the two
  shell-driven suites surface both files-on-disk and real-cases
  counts), structural shape (packages, binaries, demo services,
  man pages), the feature surface (config directives + wire
  opcodes grepped straight from source so no built binary is
  required), and doc size (CHANGELOG versions + line counts).
  Text default, `--json` for CI, `--markdown` for README embed.
  Excluded from the slpkgs template on purpose — dev tooling,
  not something that ships to an operator's rootfs. Landed as
  `2ac5212`.

### Docs

- README.md — full refresh (`0421dc9`, +307/-17 LOC). Journal
  pipeline + journalctl 65/65 parity moved out of "deliberately
  out of scope" into the Features list, with the full flag
  inventory. New Features bullets for self-introspection,
  migration converters, `slinitctl analyze`, and boot recovery
  UX. Building section lists 8 new binaries. Companion Tools
  gains 7 new sections (journalctl / journald / journal-migrate
  / supports / three converters) with concrete example
  commands. Project structure enumerates the 6 new `cmd/` dirs
  + 8 new `pkg/` dirs. Roadmap adds Phases 41-54 covering
  v2.1.0 → v2.1.12.
- doc/man/slinit.8.md — description block lists upstart +
  systemd as additional feature sources; explicit journalctl +
  FSS-sealing callout; explicit out-of-scope list; new
  paragraph naming the three migration converters.
- CONTRIBUTING.md — test counts refreshed (218 / 218 / 1956);
  `pkg/` + `cmd/` enumerations bumped to today's 29 packages +
  35 binaries with pointers to `ls` for the live list.
- CLAUDE.md — verification-command comment refreshed to the
  actual test counts.
- tests/functional/README.md — 202-218 case rows added; count
  bumped 201 → 218.
- tests/acceptance/ssh/README.md — case-count phrasing bumped
  to 218 real cases (219 files on disk with `999-cleanup`).

## [2.1.13] — 2026-08-08

Test-suite catch-up. Closes the SSH-acceptance + QEMU-functional
coverage gap for every feature landed between v2.0.0 and v2.1.12
that can be exercised at runtime. No code changes; assertion tests
only.

### Added — tests/acceptance/ssh (197 → 219 cases)

22 new cases (198-219) covering:
- Converters (v2.1.4-5): runit basic + log companion (v2.1.5
  headline), openrc simple + wrapped, systemd Type/Restart/deps
  with suffix stripping.
- Journalctl group A-B (v2.1.6/7/10): identifier `-t/-T` with
  the v2.1.7 small-limit fix, `--vacuum-*` with current-day
  preservation + missing-dir tolerance.
- Journalctl C-D-E (v2.1.8-9): invocation tracking +
  `--list-invocations`, catalog round-trip via `--root`,
  `--setup-keys` + `--force` safety.
- Journalctl Sprints 3-4 (v2.1.11-12): `--namespace` + tag +
  `--list-namespaces`, `--image` + `--image-policy=strict`.
- v2.1.0-2 baseline: `slinit-supports`, `-b` shortcut, `-k`
  kernel events + render rules, `slinitctl analyze`
  (time/blame/critical-chain/dot/plot stub), journalctl symlink,
  verbose+export formats, `slinitctl disable` dual-wire,
  bracket target-PID rule.
- Deep dinit + FSS (v2.1.0): env-var compat
  (SLINIT_SERVICENAME + DINIT_SOCKET_PATH), FSS binary
  `--verify` clean + tamper detection, backlog replay banner.

Also lands a chmod-only sweep marking cases 170-197 executable
so an operator running `./cases/NN-…sh` directly does the right
thing (previously only mattered because run.sh invokes them via
`sh $file`).

### Added — tests/functional (201 → 218 cases)

17 new cases (202-218) covering the same feature surface from
the fresh-PID-1-boot side. Groups roughly mirror the acceptance
batch:
- 202 `--list-boots` + `-b` shortcut.
- 203 kernel events + render rules (gates on kmsg presence so a
  silent QEMU boot doesn't false-fail).
- 204 `slinit-supports` introspection.
- 205 `slinitctl analyze` subcommands + `plot` stub.
- 206 runit converter + log companion.
- 207 openrc converter (variable-only + wrapped paths).
- 208 systemd converter (Type/Restart/User+Group, line
  continuation, forking→bgprocess, oneshot restart default).
- 209 invocation tracking (fresh-boot version — exact count
  assertions).
- 210 catalog round-trip via `--root` prefix.
- 211 FSS `--setup-keys` + `--force` safety gate.
- 212 FSS binary `--verify` full tamper-detection round-trip.
- 213 dinit env-var compat.
- 214 `slinitctl disable` dual-wire (tolerant symlink probe;
  layout differs between guest and ceres).
- 215 journalctl verbose + export formats.
- 216 Group A bundle (`--fields` / `--header` / `--disk-usage`
  / `-F` / `--utc` / `--no-hostname` / `--output-fields` /
  `-g`).
- 217 vacuum + flush (direct file vacuum + spawned daemon for
  `--flush` / `--relinquish-var` via admin socket).
- 218 namespace daemon + `--list-namespaces` + filter (synthetic
  JSONL for the tag assertion since the QEMU minimal boot may
  not produce enough backlog events for the namespaced daemon
  to persist bytes in time).

`build-vm.sh` extended to install six additional binaries in the
guest so the new cases have something to exercise: `slinit-
supports`, `slinit-journalctl`, `slinit-journald`, and the three
converters. Also adds a `journalctl → slinit-journalctl` symlink
matching the slpkgs `post_install` convention.

### Not covered — reserved for manual QA / future harness work

- Boot debugger Ctrl-B (v2.1.1) — interactive tty.
- Rescue menu / Emergency-Rescue split / tty9 debug-shell /
  confirm-spawn / crash-shell (v2.1.1-2) — interactive tty +
  fatal-boot simulation.
- Bootmode kernel-cmdline parser (v2.1.2) — needs per-test
  kernel-args injection at VM boot.
- `--image` / `--image-policy` in the QEMU harness (v2.1.12) —
  needs `mkfs.ext4` in the Alpine minirootfs; acceptance case
  208 covers it against ceres.

## [2.1.12] — 2026-08-08

**Systemd journalctl parity project complete: 65 of 65 flags.**
Sprint 4 lands `--image` + `--image-policy` — the last two flags
in systemd's surface. Coverage 63/65 → **65/65 (100%)**.

### Added

- `--image=PATH` — attach a disk image via `losetup(8)` (read-only
  with `--partscan`), mount the first filesystem containing a
  recognised journal directory
  (`var/log/slinit-journal` / `run/slinit-journal` /
  `var/log/journal`), query it, detach on exit.
- `--image-policy=POLICY` — accepts slinit shorthand
  (`loose` / `strict` / `""`) and systemd's full colon-separated
  per-partition form
  (`root=verity+encrypted+signed:usr=verity:home=encrypted`).
  `strict` refuses LUKS / LVM / verity partitions upfront via
  `lsblk` FSTYPE probe. Full-form tokens are parsed and stored on
  the `Policy.PerPartition` map, reserved for future LUKS-aware
  slinit versions.

### Notes

Pragmatic implementation: rather than porting systemd's ~5kloc
`libblkid` + LUKS + LVM stack, `pkg/dissect` shells out to
util-linux (`losetup`, `mount`, `lsblk`) — universally available on
Linux. Trade-off: no native handling of encrypted / verity / LVM
partitions. The 95% common case (raw + GPT/MBR partitioned images
with ext4/xfs/vfat filesystems) works fully.

Detach always runs via `defer` — even on error paths — so a broken
image never leaks a loop device.

### Coverage summary — the 4-sprint parity arc

- v2.1.9 (Sprint 1, 2 flags): `--force` + `--synchronize-on-exit`
- v2.1.10 (Sprint 2, 3 flags): `--flush` + `--relinquish-var` +
  `--smart-relinquish-var` via a UNIX DGRAM control socket
- v2.1.11 (Sprint 3, 2 flags): `--namespace` + `--list-namespaces`
- v2.1.12 (Sprint 4, 2 flags): `--image` + `--image-policy`

Total from v2.1.8: 58/65 → 65/65. Every flag accepted; five carry
slinit-specific semantics (documented in --help):
- `--force`: always-overwrite for `--setup-keys`.
- `--synchronize-on-exit`: always-on (sinks fsync on Close).
- `--sync`: SIGUSR1 to journald PID (systemd uses dbus).
- `--merge`: no-op on single-source setups.
- `--pager-end`: no-op (no pager wired).

## [2.1.11] — 2026-08-08

Sprint 3 of the systemd journalctl parity follow-up: journal
namespaces. Coverage 61/65 → **63/65 (~97%)**.

### Added

- `--namespace=NS` — filter events by the new `Event.Namespace`
  field (systemd `LogNamespace=` equivalent). Uses the same
  server-side push-down + client-side re-filter pattern the rest
  of the Group A filters use; small `-n` limits stay correct.
- `--list-namespaces` — enumerate namespaces detected via
  `/var/log/slinit-journal.*` and `/run/slinit-journal.*`
  directories. The default (unnamed) namespace is implicit and
  not listed.
- `slinit-journald --namespace=NS` — when set, any default path
  flag still at its compiled-in value gets a `.NS` suffix so two
  daemons with different namespaces never fight over the same
  files. Explicit path overrides always win. `guardedSink.namespace`
  tags incoming events so downstream storage + queries can filter
  uniformly.

### Wire changes

- `journal.Event` gains `Namespace string` (zero-value = default
  namespace).
- `control.JournalQueryRequest`, `journal.QueryFilter`,
  `QueryFilter.isEmpty`, and `Match` all gain the field.
  `wireLimitFor` bypasses server `-n` when the new filter is set.

### Notes

Services still emit through slinit's default event bus + ring
buffer without a namespace tag. Namespaces are a journald-side
concept — the operator wants isolation on the storage side, not
on slinit's in-memory ring. Adding `log-namespace =` as a
per-service config directive is a natural follow-up but not
shipped here to keep this cut focused on the client surface.

Remaining 2 systemd flags need substantial infrastructure and land
in Sprint 4:
- `--image` / `--image-policy` — disk dissection library port
  (LUKS + LVM + GPT + FS mounting via loop devices).

## [2.1.10] — 2026-08-08

Sprint 2 of the systemd journalctl parity follow-up: three flags
for volatile ⇄ persistent switching operators need before umount
/var. Coverage 58/65 → **61/65 (~94%)**.

### Added

- `--flush` — asks slinit-journald to migrate any journal files
  from the volatile fallback dir (typically `/run/slinit-journal`)
  to the persistent primary (typically `/var/log/slinit-journal`)
  and switch the active sink over. No-op when the daemon is already
  writing to the primary.
- `--relinquish-var` — closes the persistent sink and reopens at
  the volatile fallback. Call before umount /var so nothing pins
  the persistent filesystem.
- `--smart-relinquish-var` — probe `/proc/self/mountinfo` for a
  `/var` mount line first; only relinquish if `/var` is on a
  distinct filesystem. On single-fs systems this becomes a
  documented no-op.

### Wire changes

- **pkg/journald/flush.go** (new) — `Migrate(src, dst)` moves the
  journal artefact set with a same-fs Rename fast path + cross-fs
  copy+remove fallback. `ProbeWritable(dir)` MkdirAll + write-then-
  remove probe.
- **cmd/slinit-journald** — `guardedSink` wraps the active sink
  with a mutex + factory closure that knows how to reopen at any
  directory; `FlushVolatile` / `RelinquishVar` swap the inner sink
  under the lock so the Receiver's Handle loop never races the
  swap. `--admin-socket` flag (default `/run/slinit-journald.ctl`)
  plus a `runAdminSocket` goroutine that reads
  `flush` / `relinquish-var` / `smart-relinquish` datagrams and
  dispatches to the guarded sink.
- **cmd/slinit-journalctl** — the three flags dial the admin socket
  and send a single command word (fire-and-forget, same semantics
  as SIGUSR1 / SIGUSR2 but doesn't hit Go's os/signal SIGRTMIN
  delivery bug that made the original signal-based design fail).

### Notes

The three flags were originally planned as SIGRTMIN+0 / +1
handlers, but a live smoke on ceres showed Go's `signal.Notify`
doesn't deliver signals in the SIGRTMIN..SIGRTMAX range on Linux —
the process terminates with the default action even when Notify
was called for that signal. A minimal reproducer confirmed the
issue is Go-runtime, not our wiring. The DGRAM control socket
approach is both more robust and cleaner for future admin
extensions.

Remaining 4 systemd flags need substantial infrastructure and land
in Sprints 3-4:
- `--namespace` / `--list-namespaces` — journal namespace concept
  (Sprint 3).
- `--image` / `--image-policy` — disk dissection library port
  (Sprint 4).

## [2.1.9] — 2026-08-08

Sprint 1 of the follow-up systemd parity push. Two more flags at
minimal cost:

### Added

- `--force` — safety gate for `--setup-keys`. Previously we always
  overwrote an existing FSS key file, silently invalidating every
  TAG chain sealed with the old key. Now refuse without `--force`
  and print a hint that names the flag.
- `--synchronize-on-exit[=BOOL]` — accepted for parity. Slinit's
  sinks always `fsync` on `Close` (`FileSink.Close` /
  `BinarySink.Close`), so this is effectively always-on and there's
  nothing to configure. Documented as such in `--help`; kept
  parseable so scripts written for systemd don't fall over.

Coverage: 56/65 → **58/65 (~89%)**. Remaining 7 are:
- `--flush` / `--relinquish-var` / `--smart-relinquish-var` —
  volatile-persistent switching machinery.
- `--namespace` / `--list-namespaces` — journal namespace concept.
- `--image` / `--image-policy` — disk dissection library.

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

## [2.1.3]/[2.1.4] — 2026-08-06

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
