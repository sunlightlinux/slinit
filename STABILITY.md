# Stability commitments

This document says what slinit promises not to break, for how long, and
what it does instead when something has to change. It is written for
distributions and image builders deciding whether they can pin a
version, and for contributors deciding whether a change is allowed in
the release they are aiming at.

It applies from the first release that ships this file. Releases before
that were made without a written policy; the known deviations are listed
under [History](#history) so nobody has to discover them.

Which versions receive fixes is covered separately, in
[SECURITY.md](SECURITY.md#supported-versions).

## The short version

- **The control protocol stays at `CPVersion=7` until v4.0.0.** New
  commands can be added; existing ones do not change shape or meaning.
- **Service directives are not removed or reinterpreted within a major
  version.** A directive that is going away is deprecated first, keeps
  working with a warning, and is removed no earlier than the next major.
- **Service files are forward-compatible, not backward-compatible.** A
  file that loads on 2.3.0 loads on every later 2.x. A file that uses a
  directive introduced in 2.3.6 fails to load on 2.3.5 — see
  [Service configuration](#service-configuration).
- **Metric names are an interface.** A name served at `/metrics` is not
  removed or renamed within a major version, and keeps its type — see
  [Metrics](#metrics).
- **The Go packages under `pkg/` are not a public API.** Import them at
  your own risk.

## Versioning

slinit uses [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html),
applied to the *stable surfaces* listed below. The Go package layout is
not one of them.

| Release | May contain |
|---------|-------------|
| **MAJOR** (`3.0.0`) | Incompatible changes to a stable surface: removing a deprecated directive, command or flag, or changing what an existing one does. Always listed under `Removed` or `Changed` in the CHANGELOG, with the migration. |
| **MINOR** (`2.4.0`) | Everything a patch may contain, plus deprecations and new subsystems. |
| **PATCH** (`2.3.7`) | Bug fixes, and *additive* changes: a new optional directive, a new control-protocol command, a new `slinitctl` subcommand or flag. Nothing that already works changes behaviour. |

Additive changes are allowed in patch releases because slinit ships
small, frequent releases and holding every new directive for a minor
bump would hold back fixes that ride along with it. The cost is the one
stated above: a service file that uses a new directive needs at least
the version that introduced it. If you pin to a minor line, write
service files against the oldest patch you deploy, and check them with
that version's `slinit-check`.

A change that corrects slinit to match its own documentation, or to
match dinit where slinit claims dinit compatibility, is a bug fix, not
a behaviour change. If existing setups could be relying on the wrong
behaviour, it is still called out under `Changed`, with what to do, and
it goes in a minor release rather than a patch.

## Stable surfaces

### Control protocol

The binary protocol spoken over the control socket, by `slinitctl` and
by anything else that implements it (`pkg/control/protocol.go`).

- `CPVersion` stays `7` and `MinCompatVersion` stays `1` until **v4.0.0**.
  This holds across 3.x as well.
- Existing command codes and reply codes keep their numbers, payload
  layout and meaning. Commands 0–28 and replies 50–79 are dinit's, and
  stay wire-compatible with dinit's `cp_cmd` / `cp_rply` enums.
- New commands are added with new code numbers. A daemon that does not
  know a command answers `RplyBadReq`, so a newer client talking to an
  older daemon gets a clean error, not a hang or a misread reply.
- Any 2.x `slinitctl` works against any 2.x daemon for the commands both
  know. This has been true in practice since CPVersion reached 7; it is
  now a promise.
- When a command's shape has to change, it gets a *new* code and the old
  one keeps working. `CmdEnableServiceV7` (29) and `CmdRmDepV7` (30)
  sit beside the dinit originals this way.

### Service configuration

The directives documented in `slinit-service(5)`, their operators (`=`
and `:`), their accepted values, and what they do.

- A directive is not removed, renamed or given a new meaning within a
  major version.
- Aliases stay aliases. `termsignal`, `rlimit-addrspace` and
  `run-in-cgroup` are dinit spellings kept for compatibility and are
  covered by the same rule.
- A directive slinit does not recognise is a **load error**, not a
  silent skip (`pkg/config/parser.go`). That is deliberate: a typo in a
  hardening directive should stop the service, not start it unhardened.
  It is also why service files are only forward-compatible.
- New directives are listed in the CHANGELOG under the version that
  introduced them. `slinit-service(5)` is *intended* to mark each new
  directive with the version it appeared in; as of v2.3.9 it does not,
  and the CHANGELOG is the only place to find out when a directive
  arrived. Tracked as a work item below.
- The load directories (`/etc/slinit.d` and the others in `slinit(8)`)
  and the `@include`, `@include-opt` and `@meta` lines are stable in the
  same sense.

### Command line

- `slinit`'s options and the kernel command-line keys it reads, as
  documented in `slinit(8)`.
- `slinitctl`'s subcommands and flags, as documented in `slinitctl(8)`.
- `slinitctl`'s exit statuses: `0` success, `1` failure, `2` usage
  error. A subcommand that exits `0` today on an outcome does not start
  exiting non-zero on the same outcome, or the reverse, outside a major
  release — except under [Exceptions](#exceptions).
- `slinitctl show`'s `Key=Value` output: keys are not removed or
  renamed; new keys may appear. Parse it by key, not by line number.
- The same applies to the companion tools with a man page in
  `doc/man/`, for their documented flags and exit statuses.

### Metrics

The Prometheus exposition served at `/metrics` by `--metrics-listen`
(`pkg/metrics`). Anyone scraping it builds dashboards and alerting
rules on the names, which makes them an interface rather than an
implementation detail.

- A metric name is not removed or renamed within a major version, and
  a metric keeps its type. New metrics and new labels may appear.
- A counter — every name ending `_total` — stays monotonic across the
  life of the process, as Prometheus requires. It resets to zero on
  restart and never otherwise.
- The current set is `slinit_build_info`, `slinit_boot_ready`,
  `slinit_boot_kernel_seconds`, `slinit_boot_userspace_seconds`,
  `slinit_services`, `slinit_service_up`, `slinit_service_failed`,
  `slinit_service_startup_seconds`, `slinit_service_restarts_total`,
  `slinit_restarts_total`, `slinit_watchdog_restarts_total`.
- The endpoint is off unless `--metrics-listen` is given. Turning it on
  is not a promise that the process serves anything else over HTTP; the
  only paths are `/metrics` and `/`.

### On-disk formats

- **Journal files.** A newer reader reads every file an older writer
  produced, in both the JSONL format and the binary `SLJRNL01` format.
  JSONL fields may be added, never removed or retyped. The binary
  format evolves through its header flags: a writer that needs an
  incompatible change sets a new `IncompatFlags` bit, and readers refuse
  files carrying a bit they do not know (`pkg/journalbin/format.go`).
- **Persisted state** that slinit reads back across a restart or an
  upgrade (for example the persisted shutdown intent, and the
  soft-reboot snapshot) stays readable by later versions within the
  major. The snapshot is JSON with named fields, so a field may be
  added; a reader ignores what it does not know and an absent field
  means what its zero value meant before it existed.
- **Container results.** `/run/slinit/container-results/exitcode` and
  `.../haltcode`, written by `slinit -o` as it goes down, are read by
  whatever supervises the container. The file names, and the meaning of
  what is in them, are stable within the major. The exit code follows
  the same rule as `slinitctl`'s: an outcome that writes `0` today does
  not start writing non-zero, except under [Exceptions](#exceptions) —
  v2.3.8 changed one such outcome and is recorded below.

### Compatibility surfaces owned by other projects

slinit implements some interfaces that another project defines:
`org.freedesktop.login1` and `org.freedesktop.systemd1` on D-Bus
(`slinit-logind`), the `/run/systemd/{sessions,users,seats}` records
libelogind reads, the dinit protocol range above, and the OpenRC
command shims (`rc-service`, `rc-update`, `rc-status`).

For these, the upstream project's definition is the contract, not
slinit's current behaviour. A change that brings slinit closer to
upstream is a fix, even if something had started depending on the
difference. Such changes are still called out under `Changed` when they
are visible, as the move from "every login1 session is always active"
to activity following the foreground VT was.

## Not covered

- **Go packages.** Everything under `pkg/` and `cmd/` is internal to
  slinit, even where Go would let you import it. The module path has no
  `/v2` suffix, so the 2.x tags are not usable as Go module versions in
  the first place. Packages move, change signature or disappear in any
  release.
- **Human-readable output**: the wording of `slinitctl status`, `list`
  and similar, log and journal messages, `--help` text, colours and
  alignment. Scripts should use exit statuses, `slinitctl show`, or the
  control protocol.
- **Timing and performance**, beyond not regressing without reason.
- **Private runtime state**, such as `/run/slinit-logind/*.json` and the
  internals of the control socket's directory.
- **Tests, demos and tooling**: `tests/`, `demo/`, `tools/`.
- **Anything undocumented.** If a behaviour is not in a man page, the
  README or this file, it is not promised. Ask for it to be documented
  if you depend on it.

## Deprecation

When a stable interface has to go:

1. It is marked deprecated in a **minor** release, with the replacement,
   in the CHANGELOG and in its man page.
2. It keeps working. Using it should produce a warning from
   `slinit-check` and in the daemon log, so the operator hears about it
   before the removal rather than at it. As of v2.3.9 neither warns:
   nothing has been deprecated yet, so the machinery has never been
   needed, but it has to exist before anything is. Tracked below.
3. It is removed no earlier than the **next major** release, and at
   least one minor release after the deprecation.

## Not yet implemented

Two things this document describes do not exist yet. They are listed
here rather than quietly promised, because a policy that describes
machinery nobody built is worse than one that admits the gap.

| Commitment | Status as of v2.3.9 |
|------------|---------------------|
| `slinit-service(5)` marks each directive with the version it appeared in | Not started. The CHANGELOG carries the information; the man page does not. |
| A deprecated directive warns from `slinit-check` and in the daemon log | Not started. Nothing is deprecated yet, so nothing has been missed — but this has to land before the first deprecation, not with it. |

Neither blocks anything today. Both block the first deprecation.

## Exceptions

Two kinds of change may break a stable surface in any release, patch
releases included:

- **Security fixes.** If keeping the old behaviour keeps the
  vulnerability, the behaviour changes. v2.3.6's journald fix is the
  model: a sender that exits before its `/proc` snapshot now gets empty
  `_COMM`/`_EXE`/`_CMDLINE` instead of the values it claimed, because
  the claimed values were forgeable.
- **Behaviour that loses data or takes the machine down**, where the old
  behaviour cannot reasonably have been relied on.

Either way, the change is listed under `Security` or `Changed` with a
`Compat` note saying exactly what differs.

## History

Changes this policy would not have allowed:

- **v2.3.9 reclaimed a stopped service's cgroup directory in a patch.**
  slinit creates those directories and had never removed them; leaving
  them behind leaked one per `slinitctl run --slice=NAME`, whose
  transient units never reuse a name. Nothing documented promised the
  directory would outlive the service, so by the letter of the rules
  this is a leak fix rather than a behaviour change — but an external
  script that wrote into a stopped service's cgroup would now find it
  gone, and "a setup could be relying on it" is the test this policy
  actually applies. It belonged in a minor. It is in v2.3.9's `Changed`
  section with what to check.

  The same release's one-second grace for an in-flight stop-command is
  *not* listed here: `shutdown <kind> now` is documented as being
  impatient with a slow stop, and killing the cleanup script that a
  detached daemon depends on was never what that promised.

- **v2.3.8 is a patch carrying two behaviour changes.** A container that
  is told to stop a service and ends up with nothing running exits 0
  where it used to exit 1, and `slinitctl shutdown <kind> now` kills the
  services instead of being a synonym for the plain form. Under the
  rules above both belong in a minor. They were released as 2.3.8
  deliberately; the CHANGELOG entry says so at the top, and both are in
  its `Changed` section.

Changes made before this policy that it would not have allowed:

- **v2.3.5 changed `slinitctl start` in a patch release.** It used to
  return as soon as the daemon accepted the request; it now waits for
  the outcome and exits non-zero on a failed start. It was a dinit
  parity fix, but scripts that relied on the immediate return, and
  `triggered` services in particular, needed `--no-wait`. Under this
  policy it would have gone in a minor release with the same `Changed`
  note.
- **The v2.3.0 CHANGELOG says an older parser silently skips an
  unrecognised directive.** It does not: an unknown directive has been a
  load error since the parser was written. The forward-only
  compatibility described under
  [Service configuration](#service-configuration) is how it has always
  behaved.
