# slinit-service 5 "" "" "slinit \- service management system"

## NAME

slinit-service - slinit service description file format

## SYNOPSIS

*/etc/slinit.d/*\*service-name*\*

## DESCRIPTION

Each file describes a single service. The filename is the service name.
Settings are written one per line as *KEY*=*VALUE* (or, for
dependencies, *KEY*:*VALUE* — see **DEPENDENCY KEYS**). Lines beginning
with `#` are comments. Blank lines are ignored. Trailing whitespace is
stripped.

The format is backwards-compatible with **dinit** service files: every
dinit setting that has a meaningful counterpart in slinit is accepted
under the same name. slinit additionally accepts settings ported from
runit, s6-linux-init and OpenRC, as well as a small number of
slinit-specific extensions.

### Operators

**=**
:   Assign. The right-hand side replaces any previous value.

**+=**
:   Append. For list-valued settings (commands, dependencies, log
    processors, ...) appends to the existing list rather than replacing
    it. For scalars it has the same meaning as **=**.

**:**
:   Same as **=**, accepted in dependency keys for parity with dinit
    (e.g. `depends-on:network`).

### Includes

**@include** *path*
:   Inline another file at this point. Relative paths are resolved
    against the directory of the file containing the directive. Up
    to 8 levels of nesting are allowed.

**@include-opt** *path*
:   Like **@include**, but missing files are silently ignored rather
    than producing an error.

### Variable substitution

Values undergo environment-variable substitution at load time:
*$VAR*, *${VAR}*, *${VAR:-default}* and *${VAR:+alternate}* are
recognised. Use *$$* for a literal dollar. The pseudo-variable
*$1* expands to the service argument when the service is loaded
with one (e.g. `getty@tty1` → `$1` = `tty1`).

### conf.d overlays

Files dropped into */etc/slinit.conf.d/*\*service-name*\*` are loaded
*after* the main service file using the same parser, so they may
override scalars or append (`+=`) to lists. Overlays do not need to
exist; if they do, they may not change the service type.

### .override files

A file named *\*service-name*\*.override* sitting in the **same
directory** as the service file is an upstart-style drop-in: it lets
an operator tweak a distribution-packaged service without editing the
shipped file (so package upgrades don't conflict). It is parsed with
the full grammar *after* both the main file and any conf.d overlays,
so it has the final say on scalar conflicts; `+=` still appends. The
override file is optional, but if present a parse error in it is
fatal. For templates the override sits next to the resolved base file
(e.g. *worker.override* for `worker@foo`) and applies to every
instance, with *$1* substitution still in effect.

## SERVICE TYPES (`type=`)

**process**
:   A long-running supervised process. slinit forks/execs **command**
    and tracks the resulting PID directly.

**bgprocess**
:   A daemon that backgrounds itself. slinit runs **command**, waits
    for it to exit, and then reads **pid-file** to find the daemon.

**scripted**
:   A service that has a one-shot start command (and optionally a
    one-shot **stop-command**); considered started once the start
    command exits successfully.

**internal**
:   A pseudo-service with no associated process; used for grouping
    dependencies (e.g. **boot**) or as a flag.

**triggered**
:   Like **internal**, but stays in *waiting* until **slinitctl
    trigger** fires it. Useful as a manual gate.

## CORE SETTINGS

**description**=*text*
:   Human-readable description.

**author**=*text*, **version**=*text*, **usage**=*text*
:   Optional informational metadata mirroring upstart's *author*,
    *version* and *usage* stanzas. Surfaced by **slinitctl status**
    when set; otherwise ignored. They do not affect service behavior.

**command**=*program* [*args*...]
:   Program to execute (for **process**, **scripted**, **bgprocess**).
    Quoting and escaping follow shell-like rules.

**command-argv0**=*string*
:   Override the argv[0] presented to the exec'd binary. The kernel
    still exec's **command**[0] — only the name the child sees changes.
    Mirrors runit's *chpst -b* and Debian's *start-stop-daemon --startas*.
    Useful when the target inspects its own argv[0] (e.g. a shell that
    becomes a login shell when argv[0] starts with "-") or when a
    supervisor wants a distinct process name in *ps* output. Applies only
    to the main **command**; not to **stop-command**, hooks, or
    ready-check commands. **process** and **bgprocess** only.

**script** ... **end script**
:   Upstart-style inline shell sugar. A bare `script` line on its own
    opens a block; every following line is taken **verbatim** (leading
    whitespace preserved) until a bare `end script` line. The block is
    wrapped as `/bin/sh -c` *body* and becomes **command**, so it is
    mutually exclusive with **command** (specifying both is an error).
    The body undergoes the same load-time `$VAR`/`$1` substitution as
    **command**; use `$$` for a literal `$` (so a runtime shell
    variable must be written `$$VAR`). A missing `end script` is a
    fatal parse error.

**stop-command**=*program* [*args*...]
:   For **scripted**: program executed when the service stops.

**finish-command**=*program* [*args*...]
:   Runit-style: a program executed *after* **command** exits, before
    a possible restart.

**pre-start-command**=*program* [*args*...]
:   systemd-style *ExecStartPre=*: a program executed synchronously
    before the main **command** is forked. A non-zero exit fails the
    start. Runs after sandbox / required-paths checks but before
    fork+exec, so a failed pre-hook never leaves a half-started
    process behind. **+=** appends arguments. Process services only.

**post-start-command**=*program* [*args*...]
:   systemd-style *ExecStartPost=*: a program executed asynchronously
    after the service reaches **started**. A non-zero exit is logged
    but does not fail the service. Useful for "service is up, notify
    something" hooks. **+=** appends arguments. Process services only.

**ready-check-command**=*program* [*args*...]
:   A program polled until it exits 0; the service is considered
    started only when the check passes.

**ready-check-interval**=*duration*
:   How often to retry **ready-check-command**.

**pre-stop-hook**=*program* [*args*...]
:   Hook run before the stop signal is delivered.

**working-dir**=*path*
:   Working directory for the service.

**run-as**=*user*[:*group*]
:   Drop privileges to *user* (and optionally *group*) before exec.

**file-descriptor-store-max**=*N*
:   Enable the systemd-style file-descriptor store. At BringUp slinit
    creates a `$NOTIFY_SOCKET` (Unix datagram) at
    `/run/slinit/notify/`*service*`.sock` owned by the run-as user.
    The child can `sd_notify` packets like:

        FDSTORE=1
        FDNAME=upstream

    with one or more fds attached via SCM_RIGHTS; slinit keeps up to
    *N* of them. On the next BringUp the stored fds are prepended to
    `LISTEN_FDS` (with their FDNAMEs in `LISTEN_FDNAMES`), so a
    restart re-attaches the previous run's listening sockets without
    dropping connections.

    The store lives in slinit's memory — a daemon restart loses
    everything, matching systemd. `0` (default) disables the
    feature; an unconfigured service has no `$NOTIFY_SOCKET`.

**dynamic-user**=*yes*|*no*
:   When *yes*, slinit allocates a transient UID/GID from a pool
    (default range 61184..65519, matching systemd) at every
    **BringUp** and releases it in **Stopped**. The UID exists only
    in slinit's memory — there is no */etc/passwd* entry. Per-service
    isolation features (**runtime-directory** chown, **credentials**
    file ownership) all see the same transient identity.

    The pool is per-daemon and does not survive a slinit restart —
    dynamic-user is an isolation feature, not a stable identity.
    Conflicts with **run-as**: at runtime the dynamic UID wins.

    Use for short-lived workers, sandboxed scripts, and anything
    that needs a non-zero UID but doesn't merit a permanent account.

**chroot**=*path*
:   `chroot()` into *path* before exec.

**new-session**=*yes*|*no*
:   Place the service in a new POSIX session (`setsid`).

**lock-file**=*path*
:   Acquire an exclusive flock on *path* before exec; fail if held.
    Useful for "only one instance ever" semantics.

**umask**=*octal*
:   Initial umask for the service. Defaults to slinit's umask.

**term-signal**=*signal*
:   Signal sent on stop. Defaults to *TERM*. Aliases: **termsignal**
    (dinit), **stopsig** (OpenRC).

**reload-signal**=*signal*
:   Signal sent to the running process when the operator runs
    **slinitctl reload-signal** *service*. The intended use is the
    "tell the daemon to re-read its own config" idiom (e.g. nginx,
    sshd, syslog). No default — when unset, **slinitctl
    reload-signal** rejects the request with an explanatory error.

    Distinct from **slinitctl reload** *service*, which re-reads
    the slinit-side service description from disk (the slinit
    operator's view of the service) without touching the running
    process. The two operations address different layers; both can
    legitimately appear in the same operator workflow.

    Inspired by upstart's **reload signal** stanza.

## ENVIRONMENT

**env-file**=*path*
:   Read additional *KEY*=*VALUE* assignments from *path* before
    exec. Same syntax as **slinit**(8)'s **\--env-file**.

**env-dir**=*directory*
:   Read environment from `envdir`-style directory (one variable per
    file; filename is the variable name, contents the value).

**setenv** is also exposed at runtime via **slinitctl**(8).

The variables **SLINIT_SERVICENAME** and **SLINIT_SERVICEDSCDIR** are
always set in the service environment (see **slinit**(8)).

## DEPENDENCIES

slinit supports seven dependency kinds. Names accept either `=` or `:`
(`depends-on=foo` and `depends-on:foo` are equivalent).

**depends-on**=*service*
:   Hard dependency. *service* must start before this one; if it
    fails, this one fails too.

**depends-ms**=*service*
:   Milestone dependency: *service* must reach *started* once, but
    its later state does not affect this one.

**waits-for**=*service*
:   Soft dependency: starts *service* alongside this one, but does
    not block startup if *service* fails.

**prepared-by**=*service*
:   Hard dependency like **depends-on**, with one extra rule: each
    time this service restarts, *service* is restarted first. Use
    for per-execution prepare / cleanup steps that must run fresh
    every cycle (e.g. provisioning a tmpfs, refreshing credentials,
    rotating a sandbox). Avoid combining with `smooth-recovery=yes`
    — the restart cascade is what gives this dependency its value.

**before**=*service*
:   Ordering only: if both end up starting, this one starts before
    *service*. No forced activation.

**after**=*service*
:   Ordering only: if both start, this one starts after *service*.

**chain-to**=*service*
:   When this service stops normally, automatically start *service*.

**depends-on.d**=*directory*, **depends-ms.d**=*directory*, **waits-for.d**=*directory*, **prepared-by.d**=*directory*
:   Drop-in directories: every entry inside *directory* (regardless of
    type) is treated as a dependency of the corresponding kind.

## ACTIVATION

**manual**=*yes*|*no*
:   When *yes*, the service refuses every auto-activation path:
    boot-graph propagation through **waits-for** / **depends-on**,
    transitive activation as another service's dependency, and
    **slinitctl wake**. Only an explicit **slinitctl start** *service*
    activates it. Inspired by upstart's **manual** stanza.

    Implications worth knowing:

    1. A hard dependent (**depends-on**: *manual-service*) cannot
       reach STARTED until the operator first runs
       **slinitctl start** on the manual service. This is the
       intended trade-off — *manual* declares "I am opt-in", and
       the operator owns the order.

    2. A soft dependent (**waits-for**: *manual-service*) will stall
       in STARTING because *waits-for* expects the target to reach
       a terminal state (STARTED or FAILED) and a manual service
       reaches neither without operator action. Don't put manual
       services in **waits-for** chains unless you also plan to
       start them by hand before the dependent.

    3. Once the operator has explicitly started the service, normal
       lifecycle applies — restarts, smooth-recovery, dependents
       acquiring it, **slinitctl stop**, all behave as usual.

    4. Default is *no*. Setting *manual = no* is harmless and the
       same as omitting the stanza.

## RESTART POLICY

**restart**=*yes*|*no*|*on-failure*
:   Whether the service is restarted automatically on exit.

**smooth-recovery**=*yes*|*no*
:   Restart in-place without notifying dependents (useful for
    short crash-restart loops).

**normal-exit**=*STATUS*|*SIGNAL*...
:   Space-separated list of exit codes (decimal, 0–255) and signal
    names (**SIGTERM**, **TERM**, etc.) that count as a normal,
    successful exit. When the process exits with one of these,
    automatic restart is suppressed *even if* **restart**=*yes*.

    Bare numbers are always exit codes — signals must be named to
    avoid the ambiguity where a value (e.g. *15*) is both a valid
    exit code and **SIGTERM**.

    Examples:

        normal-exit = 0 2 SIGTERM    # 0 and 2 are success, so is SIGTERM
        normal-exit = SIGUSR1        # killed via SIGUSR1 → don't respawn

    The **+=** operator extends an existing list:

        normal-exit = 0
        normal-exit += SIGUSR1

    With **restart**=*on-failure* the built-in admin signals
    (**SIGHUP**, **SIGINT**, **SIGUSR1**, **SIGUSR2**, **SIGTERM**)
    plus exit code 0 already suppress respawn; **normal-exit**
    extends that set with arbitrary codes/signals the operator
    declares as success. With **restart**=*yes* there are no
    built-in suppressions, so **normal-exit** is the only way to
    tell slinit "this exit is OK".

**stop-timeout**=*duration*
:   How long to wait between **term-signal** and SIGKILL.

**start-timeout**=*duration*
:   How long the service may take to reach *started*.

**restart-delay**=*duration*
:   Delay before a restart attempt.

**restart-delay-step**=*duration*
:   If non-zero, delay grows by this step on each consecutive failure.

**restart-delay-cap**=*duration*
:   Upper bound on the restart delay.

**restart-limit-interval**=*duration*, **restart-limit-count**=*N*
:   Rate-limit: more than *N* failures inside this window puts the
    service into the *failed* state.

## SYSTEM ACTIONS (appliance basics)

When a service reaches STOPPED, slinit can optionally trigger a
system-level transition depending on whether the stop was a failure
or a clean finish. Operator-issued stops (**slinitctl stop**) never
trigger either action.

**failure-action**=*none*|*reboot*|*poweroff*|*halt*|*exit*
:   Action when the service ends in a failure state: start failed,
    process exited non-zero / on a non-administrative signal, the
    restart limit was exhausted, or a start-time timeout fired.

**success-action**=*none*|*reboot*|*poweroff*|*halt*|*exit*
:   Action when the service finishes cleanly: the process exited 0
    after reaching *started*, and no auto-restart is configured.
    Mostly useful for oneshot scripted services that drive a state
    transition the operator wants the whole system to follow.

**reboot-argument**=*string*
:   Argument forwarded to **reboot**(2) when the chosen action is
    *reboot*. Currently parsed and logged; kernel handoff via
    LINUX_REBOOT_CMD_RESTART2 is a follow-up.

**runtime-max-sec**=*duration*
:   Hard cap on the total time the service may stay in STARTED.
    When the timer fires the service is asked to stop via the same
    path an operator stop uses (so **stop-command** runs and
    **stop-timeout** / SIGKILL escalation still apply). Useful for
    self-terminating workloads and for mitigating slow leaks
    without an external supervisor. The clock starts when the
    service enters STARTED; restarts reset it.

**load-credential**=*NAME*:*PATH*, **set-credential**=*NAME*:*VALUE*
:   Per-service credentials materialised at
    `/run/credentials/`*service*`/`*NAME* and exposed to the
    service through `$CREDENTIALS_DIRECTORY`. **load-credential**
    copies a file from disk; **set-credential** writes the literal
    value (no escape interpretation — use **load-credential** when
    the literal needs newlines or NULs).

    The directory is a fresh tmpfs (`size=1M`, `mode=0700`,
    `MS_NOSUID|MS_NODEV|MS_NOEXEC`), each file is `mode 0400`
    chowned to the **run-as** user, and the tmpfs is remounted
    read-only before exec. The mount is torn down when the service
    stops.

    Multiple credentials may be added with **+=** or by repeating
    the setting; ordering is preserved. Credential names may not
    contain `/` or NUL.

    *Out of scope (v1):* `load-credential-encrypted` (TPM-sealed
    secrets), `import-credential` (inheritance from the daemon's
    own credentials). Plain on-disk and inline forms cover the
    immediate "secrets without env leakage" use case.

    Example:

        load-credential = api-key:/etc/myservice/api.key
        set-credential  = greeting:hello world

**oom-policy**=*continue*|*stop*|*kill*
:   Reaction to a cgroup v2 OOM kill in the service's cgroup.

    *continue* (default) — slinit takes no action; the kernel's
    OOM kill stands as the only response.

    *stop* — slinit asks the service to stop via the normal
    stop path (stop-command, stop-timeout, SIGKILL escalation),
    so the surviving processes do not get left in an inconsistent
    state.

    *kill* — slinit SIGKILLs every process in the cgroup subtree
    using **cgroup.kill** when available (kernel ≥ 5.14) and a
    PID walk as fallback.

    The watcher samples **<cgroup>/memory.events** once per second
    while the service is STARTED; *continue* is a no-op and arms no
    watcher. Requires a configured **cgroup-path** (or a default
    via the daemon's **--cgroup-path**); otherwise the policy is
    parsed and stored but cannot fire.

The values map onto the same shutdown machinery used by
**slinitctl shutdown**: *reboot* / *poweroff* / *halt* go through
**InitiateShutdown** with the corresponding type. *exit* terminates
slinit itself when it is running as a user/container supervisor;
in PID 1 mode it is logged and ignored (the kernel would panic on
PID 1 exit).

Example — appliance that reboots when a critical service fails:

    failure-action = reboot

Example — boot-time provisioning oneshot that triggers a reboot to
apply OS-level changes:

    type           = scripted
    command        = /sbin/apply-update
    success-action = reboot

## LOGGING

**logfile**=*path*
:   Append the service's stdout/stderr to *path*. Implies
    **log-type=file** when not set explicitly.

**log-type**=*none*|*file*|*buffer*|*pipe*|*command*
:   *none*: drop output; *file*: append to **logfile**; *buffer*:
    keep an in-memory ring buffer (queryable via **slinitctl
    catlog**); *pipe*: pipe to **shared-logger**; *command*: pipe to
    **output-logger** / **error-logger**.

**log-buffer-size**=*N*
:   Size in bytes of the in-memory log buffer.

**logfile-permissions**=*octal*, **logfile-uid**=*N*, **logfile-gid**=*N*
:   File mode and ownership of the log file (created if missing).

**logfile-max-size**=*bytes*, **logfile-max-files**=*N*, **logfile-rotate-time**=*duration*
:   Built-in log rotation. Rotates when the file exceeds *bytes* or
    *duration* has elapsed; keeps the last *N* rotated files.

**log-include**=*regex*, **log-exclude**=*regex*
:   Filter lines that reach the log target. Multiple patterns OR
    together.

**log-rate-limit-interval**=*duration*, **log-rate-limit-burst**=*N*
:   Token-bucket rate limit for the log pipeline. At most *N* lines
    are kept per *duration* window; excess lines are dropped and a
    single notice is emitted when the limit first engages. Both
    must be > 0 to enable; the bucket refills proportionally as
    time passes. Requires a configured **logfile** (the limiter
    sits on the LogRotator's pipe).

**log-level-max**=*emerg*|*alert*|*crit*|*err*|*warn*|*notice*|*info*|*debug*
:   Drop log lines whose syslog severity is higher than the
    threshold. Lines without a `<N>` priority prefix are treated as
    *info* (6), so plain text passes any threshold >= info. Accepts
    the kebab-case keyword (`warning`, `error` are also accepted)
    or the numeric 0..7. *off* / *none* / *any* disable the filter.

**log-max-line-length**=*N*
:   svlogd(8) `-l N`-style hard cap on line length, in bytes. Lines
    whose content exceeds *N* are truncated to the first *N* bytes and
    marked with a `+` immediately before the trailing newline, so the
    operator can tell at a glance that the line was clipped. A
    producer that emits *N* bytes without a newline in sight (the
    common runaway case) triggers the same truncate + marker, then the
    LogRotator silently discards further input until it sees the next
    newline — guarding against unbounded lineBuf growth. Minimum
    accepted value is 16 bytes; 0 disables the cap. Requires a
    configured **logfile**.

**log-sanitize**=*char*, **log-sanitize-extra**=*bytes*
:   svlogd(8)-style byte scrubbing. When **log-sanitize** is set to a
    single printable ASCII character, each control byte in the log
    stream (values < 0x20 and 0x7F) is replaced with it, except **LF**
    (line boundary) and **TAB** (indentation) which pass through.
    **log-sanitize-extra** flags additional bytes to replace with the
    same character — one entry per byte in the string, e.g.
    `log-sanitize-extra = |;` scrubs `|` and `;` too. Setting only
    **log-sanitize-extra** implies a default replacement of `_`.
    Useful for stripping ANSI colour escapes, NULs, and other
    control-plane noise before lines land in the on-disk log.
    Requires a configured **logfile** (the scrub sits on the
    LogRotator).

**log-processor**=*program* [*args*...]
:   Pipe lines through *program* before they hit the log target.

**output-logger**=*program* [*args*...], **error-logger**=*program* [*args*...]
:   Spawn separate child processes for stdout / stderr. Implies
    **log-type=command**.

**shared-logger**=*service*
:   Send output to the named logger service (a one-to-many fan-in,
    line-prefixed with `[svc-name]`). Implies **log-type=pipe**.

**shared-logger-lossy**=*bool*, **shared-logger-queue-size**=*N*
:   svlogd(8) `-L`-style drop-on-backpressure — set these on the
    **logger service itself**, not on producers. With
    **shared-logger-lossy=yes**, producer output flows through an
    internal buffered channel; when the channel is full (typically
    because the logger process is slow to drain its stdin), the
    newest lines are dropped rather than blocking the producer.
    The mux periodically emits a synthetic
    `[shared-logger] dropped N lines` heartbeat so the loss is
    visible in the log stream. **shared-logger-queue-size** tunes
    the channel depth (default 1024 lines) — deeper smooths larger
    bursts at the cost of more memory. First producer to register
    wins the setting: the mux inherits its policy from the logger
    at construction time and does not re-tune on later joiners.

**catch-all** logging is configured at the daemon level, not here:
see **slinit**(8) `\--catch-all-log` and `--no-catch-all`.

## PROCESS MANAGEMENT

**pid-file**=*path*
:   For **bgprocess**: file the daemon will write its PID to.

**ready-notification**=*spec*
:   How the service signals readiness. Supported forms:

    * `pipefd:N` — write a single byte to fd *N*; slinit closes the
      read end on receipt unless **watchdog-timeout** is also set.
    * `pipevar:VARNAME` — slinit allocates an fd, sets *VARNAME* in
      the service environment, and the child writes to that fd.
    * `s6` — s6-style readiness on fd 1 (close stdout).

**watchdog-timeout**=*duration*
:   Per-service software watchdog. Reuses the **ready-notification**
    pipe: after the initial readiness byte the pipe stays open and
    every subsequent write is treated as a keepalive that resets the
    timer. If no keepalive arrives within *duration*, slinit declares
    the service unhealthy and stops it — the configured **restart**
    policy then handles the re-spawn. Closing the pipe while the
    service is still running counts as a miss (carrier-grade init
    does not let services silently disable their own watchdog).

    Requires **ready-notification** to be set on a **type=process**
    service; otherwise the service refuses to load. Accepts Go
    duration strings (*30s*, *2m*) or bare seconds (*30*, *0.5*).

    Use case: telco / 5G / digital-call workloads where a stuck
    service must be detected and restarted without operator
    intervention. The daemon-level **\--watchdog-device** in
    **slinit**(8) is the system-wide complement; together they cover
    "stuck slinit" and "stuck service" failure modes.

**close-stdin**=*yes*|*no*, **close-stdout**=*yes*|*no*, **close-stderr**=*yes*|*no*
:   Close the corresponding standard file descriptor before exec.

## SERVICE DIRECTORIES

systemd-style auto-managed directories. Each setting takes one or more
space-separated **relative** names (absolute paths and `.`/`..`
components are rejected; `$1`/`$VAR` are expanded). slinit creates each
directory (parents included) before the service starts, sets its mode,
and — when **run-as** is set — chowns it to that user/group. A creation
failure fails the start.

**runtime-directory**=*name*...
:   Created under */run*. **Removed when the service stops** (subject
    to **runtime-directory-preserve**).

**state-directory**=*name*...
:   Created under */var/lib*. Persistent (never auto-removed).

**cache-directory**=*name*...
:   Created under */var/cache*. Persistent.

**logs-directory**=*name*...
:   Created under */var/log*. Persistent.

**configuration-directory**=*name*...
:   Created under */etc*. Persistent.

**runtime-directory-mode**=*octal*, **state-directory-mode**=*octal*, **cache-directory-mode**=*octal*, **logs-directory-mode**=*octal*, **configuration-directory-mode**=*octal*
:   Mode for the corresponding directories (default *0755*).

**runtime-directory-preserve**=*no*|*yes*|*restart*
:   *no* (default) removes **runtime-directory** every time the service
    stops; *restart* keeps it across a restart but removes it on a full
    stop; *yes* never removes it.

## FILESYSTEM SANDBOX (Linux)

systemd-style declarative filesystem isolation. Any stanza below implies
a private mount namespace — slinit auto-OR's *CLONE_NEWNS* into the
clone flags and **slinit-runner** sets up the requested mounts there
before exec'ing the service. The host filesystem is untouched.

**private-tmp**=*yes*|*no*
:   Replace */tmp* and */var/tmp* with a fresh per-service *tmpfs*. The
    service sees an empty *tmp*; the host sees nothing. Equivalent to
    systemd's **PrivateTmp=**.

**protect-system**=*no*|*yes*|*full*|*strict*
:   *yes* read-only remounts */usr*, */boot* and */efi*. *full* adds
    */etc*. *strict* remounts the whole root */* read-only; only the
    paths listed in **read-write-paths** plus the standard
    runtime mountpoints (*/dev*, */proc*, */sys*, */run*, */tmp*,
    */var/tmp*) stay writable. *no* (default) disables the remount.

**read-only-paths**=*path*...
:   Bind-mount each absolute path on top of itself and remount it
    read-only inside the sandbox. Repeatable with `+=`. Paths must be
    absolute and free of `..` components.

**read-write-paths**=*path*...
:   Punch a writable hole through **protect-system**=*strict* for the
    listed paths. Applied before **read-only-paths**, so a path may
    appear in both (rw first wins). Repeatable with `+=`.

**protect-home**=*no*|*yes*|*read-only*|*tmpfs*
:   Hide */home*, */root* and */run/user* from the service. *yes*
    over-mounts each with an inaccessible (mode 0000) tmpfs; *read-only*
    ro-remounts them; *tmpfs* replaces them with empty per-service
    tmpfs. Equivalent to systemd's **ProtectHome=**.

**inaccessible-paths**=*path*...
:   Hide each absolute path behind an empty inaccessible mount (an
    empty tmpfs for directories, */dev/null* bind for files). The
    service cannot read or list the original content. Repeatable with
    `+=`.

**protect-proc**=*default*|*noaccess*|*invisible*|*ptraceable*
:   Remount */proc* with the matching **hidepid=** option:
    *noaccess* hides PID entries of other users; *invisible* hides them
    entirely; *ptraceable* shows only PIDs the service can ptrace.
    Equivalent to systemd's **ProtectProc=**.

**proc-subset**=*all*|*pid*
:   Remount */proc* with **subset=pid** so the service sees only the
    per-PID directories — kernel knobs (*/proc/sys*, */proc/kcore*,
    etc.) become invisible. Combines with **protect-proc** in a single
    remount. Equivalent to systemd's **ProcSubset=**.

**bind-paths**=*src*|*src*:*dst*...
:   Bind-mount *src* onto *dst* (writable) inside the sandbox. If only
    *src* is given, *dst* defaults to *src*. The runner creates *dst*
    if missing. Repeatable with `+=`.

**bind-read-only-paths**=*src*|*src*:*dst*...
:   Like **bind-paths** but the resulting mount is read-only.
    Repeatable with `+=`.

**temporary-filesystem**=*path*|*path*:*options*...
:   Mount a fresh *tmpfs* at *path*; *options* (comma-separated) is
    forwarded to **mount**(2) verbatim, e.g. *size=64m,mode=0700*.
    Repeatable with `+=`. Equivalent to systemd's
    **TemporaryFileSystem=**.

## SECCOMP FILTER (Linux)

systemd-style seccomp-bpf filter installed in the child task just
before exec. Setting any stanza below auto-implies
**PR_SET_NO_NEW_PRIVS**, which the kernel requires for a non-root
seccomp install. The runner compiles the resolved list into BPF via
the internal **seccomp** package and installs it via the
**seccomp**(2) syscall; an install failure aborts the start.

**system-call-filter**=*item*...
:   Each *item* is one of: a syscall name (e.g. *openat*), a curated
    group token (e.g. *@system-service*), or a leading **~** prefix
    *only on the first item* to switch from allowlist (default) to
    denylist mode. The list is composable with `+=` across multiple
    lines. Unknown syscall names or groups are rejected at parse time.
    Equivalent to systemd's **SystemCallFilter=**.
    Predefined groups: *@system-service*, *@privileged*, *@network-io*,
    *@file-system*, *@process*, *@clock*, *@debug*, *@ipc*, *@mount*,
    *@raw-io*, *@reboot*, *@swap*.

**system-call-architectures**=*name*...
:   Architectures the filter accepts: *native* (default — the running
    arch), *x86-64* / *amd64*, *x86* / *i386*, *arm64* / *aarch64*,
    *arm*. Syscalls issued from any other architecture take the kill
    action, mirroring systemd's **SystemCallArchitectures=**.

**system-call-error-number**=*kill*|*log*|*trap*|*errno-name*|*errno-number*
:   Action for syscalls that do NOT match an allow entry (or DO match
    a deny entry). *kill* (default) sends **SECCOMP_RET_KILL_PROCESS**;
    *log* / *trap* use the corresponding seccomp returns; an errno
    name (e.g. *EPERM*) or numeric value (1..4095) returns
    **SECCOMP_RET_ERRNO** with that value. Equivalent to systemd's
    **SystemCallErrorNumber=**.

**system-call-log**=*item*...
:   Syscalls (names or *@group* tokens) that always trigger
    **SECCOMP_RET_LOG** independent of the main filter. Useful for
    observing a process before tightening the filter. Repeatable with
    `+=`. Equivalent to systemd's **SystemCallLog=**.

## RESTRICT*/PROTECT* HARDENING (Linux)

systemd-style hardening knobs. Each is a yes/no toggle. Active knobs
expand at runner-side to a fixed seccomp deny filter (installed
alongside any user **system-call-filter** — the kernel picks the most
restrictive action across all loaded filters) and/or a small mount
operation. **PR_SET_NO_NEW_PRIVS** is auto-implied when any knob is
set. The mount-based knobs (**protect-kernel-tunables**,
**protect-control-groups**, **protect-kernel-logs**) additionally
auto-imply *CLONE_NEWNS* so the operations are confined to the
service's private mount namespace.

**protect-kernel-tunables**=*yes*|*no*
:   Read-only remount */proc/sys* and deny **iopl**(2), **ioperm**(2),
    **swapon**(2), **swapoff**(2). Equivalent to systemd's
    **ProtectKernelTunables=**.

**protect-kernel-modules**=*yes*|*no*
:   Deny **init_module**(2), **finit_module**(2),
    **delete_module**(2). Equivalent to systemd's
    **ProtectKernelModules=**.

**protect-kernel-logs**=*yes*|*no*
:   Deny **syslog**(2) and bind-mount */dev/null* over */dev/kmsg*.
    Equivalent to systemd's **ProtectKernelLogs=**.

**protect-clock**=*yes*|*no*
:   Deny **clock_settime**(2), **clock_adjtime**(2),
    **settimeofday**(2), **adjtimex**(2). Equivalent to systemd's
    **ProtectClock=**.

**protect-control-groups**=*yes*|*no*
:   Read-only remount */sys/fs/cgroup*. Equivalent to systemd's
    **ProtectControlGroups=**.

**protect-hostname**=*yes*|*no*
:   Deny **sethostname**(2), **setdomainname**(2). Equivalent to
    systemd's **ProtectHostname=**.

**lock-personality**=*yes*|*no*
:   Deny **personality**(2). Equivalent to systemd's
    **LockPersonality=** (slinit blanket-denies the call; systemd
    arg-checks for non-current personalities — the practical effect
    is identical for almost every workload).

**Not yet implemented:** the systemd hardening knobs that require
seccomp argument inspection — **RestrictRealtime=**,
**RestrictSUIDSGID=**, **MemoryDenyWriteExecute=**,
**RestrictNamespaces=**, **RestrictAddressFamilies=** — are deferred
to a v2 that grows the slinit seccomp BPF compiler with
*seccomp_data.args* support. Tracked in
*project_systemd_analysis.md*.

## NAMESPACES (Linux)

**namespace-pid**=*yes*|*no*, **namespace-mount**=*yes*|*no*,
**namespace-net**=*yes*|*no*, **namespace-uts**=*yes*|*no*,
**namespace-ipc**=*yes*|*no*, **namespace-user**=*yes*|*no*,
**namespace-cgroup**=*yes*|*no*
:   Create the corresponding new namespace before exec.

**namespace-uid-map**=*inside outside count*, **namespace-gid-map**=*inside outside count*
:   ID mappings written into */proc/PID/uid_map* and */proc/PID/gid_map*
    when **namespace-user=yes**. Multiple lines may be appended with
    `+=`.

## CGROUPS (cgroup v2)

**cgroup**=*path* (alias **run-in-cgroup**)
:   Cgroup path the service is moved into before exec. May be
    relative — resolved against **slinit**(8)'s **\--cgroup-path**.

**cgroup-memory-max**=*N*, **cgroup-memory-high**=*N*,
**cgroup-memory-min**=*N*, **cgroup-memory-low**=*N*,
**cgroup-swap-max**=*N*
:   Memory controller knobs (write-through to *memory.max*, *memory.high*, …).

**cgroup-pids-max**=*N*
:   pids.max.

**cgroup-cpu-weight**=*N*, **cgroup-cpu-max**=*spec*
:   cpu.weight, cpu.max.

**cgroup-io-weight**=*N*
:   io.weight.

**cgroup-cpuset-cpus**=*list*, **cgroup-cpuset-mems**=*list*
:   cpuset.cpus, cpuset.mems.

**cgroup-hugetlb**=*size N*
:   hugetlb.\<size\>.max — e.g. `cgroup-hugetlb=2MB 4`.

**cgroup-setting**=*file value*
:   Generic write to any controller knob: *file* is the basename
    inside the cgroup directory (no slashes, no `..`), *value* is
    the literal contents to write.

## RESOURCE LIMITS

**rlimit-nofile**=*spec*, **rlimit-core**=*spec*,
**rlimit-data**=*spec*, **rlimit-as**=*spec* (alias **rlimit-addrspace**)
:   Each *spec* is *soft*[:*hard*]. The literal `unlimited` is also
    accepted.

**nice**=*-20..19*
:   Process scheduling niceness.

**oom-score-adj**=*-1000..1000*
:   Linux OOM-killer adjustment.

**umask**=*octal*
:   File-creation mask for the service process, in octal (`000`..`777`,
    e.g. `027` or `0077`). When unset the service inherits slinit's own
    umask (set via the `--umask` daemon flag, default `0022`).

**ioprio**=*spec*
:   Linux I/O priority, e.g. `realtime:4`.

**cpu-affinity**=*list*
:   CPU affinity, e.g. `0-3` or `0,2,4`.

## REAL-TIME SCHEDULING (telco / 5G data plane)

slinit can configure the kernel scheduler for jitter-sensitive
workloads via **sched_setattr**(2). The scheduler primitives below
require **CAP_SYS_NICE** (or a sufficient **RLIMIT_RTPRIO** /
**RLIMIT_NICE**) at start time; without them the post-fork attr step
fails best-effort and the service starts with the default policy.

A runaway RT task can starve the scheduler. **sched-reset-on-fork**
defaults to *yes* so any **fork**(2) the service issues drops back to
**SCHED_OTHER** — a build script or shell exec'd by an RT service
will not inherit FIFO priority.

**sched-policy**=*fifo*|*rr*|*deadline*|*batch*|*idle*|*other*
:   Kernel scheduling policy. *fifo* and *rr* are the classic real-time
    classes (priority 1-99); *deadline* (Linux 3.14+) is bandwidth-
    reservation EDF. *batch* / *idle* are throughput-friendly variants
    of OTHER. Aliases: *realtime* → *fifo*, *normal* → *other*. Unset
    means "inherit slinit's policy".

**sched-priority**=*1..99*
:   Static priority for **SCHED_FIFO** / **SCHED_RR**. Required when
    those policies are selected; rejected for any other policy.

**sched-runtime**=*duration*, **sched-deadline**=*duration*, **sched-period**=*duration*
:   **SCHED_DEADLINE** parameters. All three are required and must
    satisfy *runtime* ≤ *deadline* ≤ *period*. Accept Go duration
    strings (*500us*, *5ms*) or bare nanosecond integers. The kernel
    runs admission control: a deadline reservation that does not fit
    the system's available bandwidth is rejected at start time.

**sched-reset-on-fork**=*yes*|*no*
:   Set **SCHED_FLAG_RESET_ON_FORK** so children fork()ed by the
    service drop to **SCHED_OTHER**. Default *yes*. Only set *no* if
    you have a specific reason — e.g. a service that uses worker
    threads via a privileged thread pool you want to keep at RT
    priority. Note: Linux applies the reset *only across fork*, not
    across **execve**(2).

Example — a low-jitter mediation service pinned to CPU 3 with
**SCHED_FIFO/80**:

```
type        = process
command     = /usr/bin/mediator
cpu-affinity = 3
sched-policy   = fifo
sched-priority = 80
```

Example — a 200µs-out-of-every-1ms reservation via **SCHED_DEADLINE**:

```
type           = process
command        = /usr/bin/rt-loop
sched-policy   = deadline
sched-runtime  = 200us
sched-deadline = 800us
sched-period   = 1ms
```

## MEMORY LOCKING & NUMA PLACEMENT

These two settings address the second pillar of jitter elimination
(after **REAL-TIME SCHEDULING**): keeping the service's working set
resident and on the right NUMA node. Both are applied by an exec
helper (**slinit-runner**) that slinit transparently prepends to the
service command — the running process is the real binary, not the
helper, so signals and PIDs match what slinitctl reports.

**slinit-runner** must be on **PATH** or in the same directory as the
**slinit** binary. When it cannot be located, mlockall and
numa-mempolicy are silently ignored (slinit logs a startup warning).

**mlockall**=*current*|*future*|*both*|*onfault*|*no*
:   Lock the service's pages in RAM via **mlockall**(2). *current*
    locks already-mapped pages, *future* locks every page mapped after
    the call, *both* combines them. *onfault* (Linux 4.4+) defers the
    lock until the page is faulted in. Comma- or `+`-separated
    combinations are accepted (`current+future+onfault`). *yes* is an
    alias for *both*. Requires **CAP_IPC_LOCK** or sufficient
    **rlimit-memlock**; without those, the service fails to start.

**numa-mempolicy**=*bind*|*preferred*|*interleave*|*local*|*default*
:   NUMA memory-allocation policy applied via **set_mempolicy**(2).
    *bind* hard-restricts allocation to **numa-nodes**; *preferred*
    tries those nodes first but allows fallback; *interleave*
    round-robins across them; *local* allocates from whatever node the
    thread is running on at allocation time; *default* clears any
    inherited policy. *bind*, *preferred*, *interleave* require
    **numa-nodes**.

**numa-nodes**=*list*
:   Comma- or hyphen-spec like *0-3* or *0,2,4*. Required for
    mempolicy *bind*/*preferred*/*interleave*; rejected for *local*
    and *default*.

Example — a 5G mediation service pinned to NUMA node 0, locking all
its pages:

```
type           = process
command        = /usr/bin/mediator
cpu-affinity   = 0-3
numa-mempolicy = bind
numa-nodes     = 0
mlockall       = current+future
```

## CAPABILITIES & SANDBOXING

**capabilities**=*caps*
:   Comma-separated list of Linux capabilities to retain (e.g.
    `cap_net_bind_service,cap_chown`). Unlisted capabilities are
    dropped from all sets including *ambient*.

**capability-bounding-set**=*caps*
:   Comma-separated positive list of capability names retained in the
    bounding set (`CapBnd`). All other capabilities are dropped via
    `PR_CAPBSET_DROP` in **slinit-runner**(8) before `execve`,
    permanently — the process cannot re-acquire them for the rest of
    its lifetime. Use this to strip capabilities the service must
    never gain, even transitively via setuid execs it later performs.
    Systemd-style `~` drop prefix is not supported; the list is
    interpreted positively (only the listed caps survive).

**securebits**=*bits*
:   Securebit names or bitmask (e.g. `keep-caps,no-setuid-fixup`).

**apparmor-load**=*path*
:   Absolute path to an AppArmor profile loaded with
    `apparmor_parser -r` *before* the service starts (so a service may
    ship its own profile). The load runs in the slinit process; if it
    fails the service start fails — a security control never silently
    degrades to unconfined. `apparmor_parser` is looked up on `PATH`
    then `/sbin/apparmor_parser`.

**apparmor-switch**=*profile*
:   Name of an AppArmor profile the service transitions into on exec
    (equivalent to libapparmor's `aa_change_onexec`). It is applied by
    **slinit-runner**, which writes `exec` *profile* to
    `/proc/self/attr/exec` immediately before `execve` — the kernel
    binds the transition to the task that performs the exec, which is
    why a parent-side apply is impossible. Requires the AppArmor LSM to
    be active; if it is not, the start fails (fail closed). Combine
    with **apparmor-load** to both ship and enter a profile.

**debug**=*bool*
:   Developer aid. When `yes`, the service is wrapped with
    **slinit-runner**, which raises `SIGSTOP` on itself after all
    runner-side setup but *before* `execve`. Attach a debugger to that
    PID (`gdb -p`), set breakpoints, then resume it with `kill -CONT`
    *pid*; the runner then performs any AppArmor transition and exec's
    the real command, so the debugger lands in the service from its
    first instruction. Off by default.

**options**=*flag* [*flag*...]
:   Space-separated boolean flags. Recognised:

    * **runs-on-console** — service owns the console (only one such service may run).
    * **starts-on-console** — service borrows the console while starting.
    * **shares-console** — does not block other console services.
    * **start-interruptible** — slinitctl stop may interrupt startup.
    * **skippable** — failure does not propagate to dependents.
    * **signal-process-only** — signal only the main PID, not the process group.
    * **always-chain** — apply **chain-to** even on failure.
    * **kill-all-on-stop** — SIGKILL the entire process group on stop.
    * **unmask-intr** — unblock SIGINT before exec.
    * **starts-rwfs** — this service marks the read-write filesystem as ready (boot bootstrap).
    * **starts-log** — this service marks the system logger as ready.
    * **pass-cs-fd** — pass the slinit control-socket fd to the child via *SLINIT_CS_FD*.
    * **no-new-privs** — set the `no_new_privs` prctl bit on the child.

**load-options**=*flag*...
:   Loader-time flags:

    * **export-passwd-vars** — export *USER*, *HOME*, *SHELL*, *LOGNAME* derived from **run-as**.
    * **export-service-name** — export *SERVICE* (legacy alias for *SLINIT_SERVICENAME*).
    * **sub-vars** — variable substitution in command args (always on; accepted for parity).

## SOCKET ACTIVATION

**socket-listen**=*path*
:   Listen on *path* (Unix socket); the listening fd is passed to
    the service via the **LISTEN_FDS** / **LISTEN_PID** convention.
    Use `+=` for multiple sockets.

**socket-activation**=*immediate*|*on-demand*
:   *immediate*: open the socket as soon as the service is loaded;
    *on-demand*: lazily start the service on the first connection.

**socket-permissions**=*octal*, **socket-uid**=*N*, **socket-gid**=*N*
:   Mode and ownership of the listening socket.

## PATH-BASED ACTIVATION

slinit can start a service when a filesystem condition is met, in the
spirit of systemd path units. The four stanzas are **mutually
exclusive** — at most one may appear per service. Each path must be
absolute. Activation is one-shot: after the trigger fires the watch is
re-armed only when the service returns to **STOPPED**.

If the configured path (or its parent, for **start-on-path-exists**)
does not exist at load time, slinit logs a warning and skips the
watch; the service remains startable via `slinitctl start`.

**start-on-path-exists**=*path*
:   Fire when *path* exists. If *path* already exists at load time the
    service starts immediately; otherwise slinit watches the parent
    directory for *basename* to appear (via **IN_CREATE** /
    **IN_MOVED_TO**).

**start-on-path-changed**=*path*
:   Fire when *path* is written and closed (file) or has entries
    created/removed/renamed (directory). Uses **IN_CLOSE_WRITE** plus
    directory mutation events.

**start-on-path-modified**=*path*
:   Like **start-on-path-changed** but also fires on every write
    (**IN_MODIFY**), not only on close.

**start-on-directory-not-empty**=*path*
:   *path* must be an existing directory. Fires when it contains at
    least one entry; if already non-empty at load, fires immediately.

## HEALTH CHECKS

**healthcheck-command**=*program* [*args*...]
:   Periodically run *program*; if it exits non-zero
    **healthcheck-max-failures** times in a row, the service is
    declared unhealthy.

**healthcheck-interval**=*duration*, **healthcheck-delay**=*duration*,
**healthcheck-max-failures**=*N*
:   Polling interval, initial delay, and consecutive-failure threshold.

**unhealthy-command**=*program* [*args*...]
:   Action to run when the service becomes unhealthy (e.g. send a
    notification, kick a circuit breaker).

## CRON-LIKE PERIODIC TASKS

**cron-command**=*program* [*args*...]
:   A sub-task that runs while the service is up.

**cron-interval**=*duration*, **cron-delay**=*duration*
:   Period and initial delay (interval mode).

**cron-on-error**=*continue*|*stop*
:   What to do when **cron-command** exits non-zero (default
    *continue*).

**cron-calendar**=*expression*
:   systemd-style **OnCalendar=** expression. When set, replaces the
    interval scheduler — fire times come from the calendar.

    Recognised forms:

    - **Aliases:** *minutely*, *hourly*, *daily* / *midnight*,
      *weekly*, *monthly*, *yearly* / *annually*.
    - **Time only:** `HH:MM`, `HH:MM:SS`. Bare time = daily.
    - **Wildcards in time:** `*:0/15` (every 15 minutes at second 0),
      `*:00` (top of every hour), `03:*` (every minute in the 3am
      hour).
    - **Weekday filters:** `Mon`, `Mon,Wed,Fri`, `Mon..Fri`. Combine
      with a time: `Mon..Fri 09:00`.
    - **Date pattern:** `YYYY-MM-DD` with `*` wildcards in any field,
      e.g. `*-*-1 00:00` (first of every month). The year is parsed
      but currently not used as a constraint.

    Out of scope: timezone shifts mid-expression, week-of-year,
    negative day-of-month.

**cron-randomized-delay**=*duration*
:   Adds uniform jitter `[0,d)` to every fire time. Useful for
    fleets that would otherwise herd on the same boundary
    (everyone backing up at midnight, etc.).

**cron-persistent**=*yes*|*no*
:   When *yes*, if the daemon was down through a scheduled fire,
    run once immediately on startup to catch up. The persistence
    store is currently in-memory only — a future on-disk store
    will let catch-up survive daemon restarts.

    Example — backup every Sunday at 03:00 with ±30 min jitter,
    catching up if a boot was missed:

        type             = scripted
        command          = /usr/local/bin/backup
        cron-command     = /usr/local/bin/backup
        cron-calendar    = Sun 03:00
        cron-randomized-delay = 30m
        cron-persistent  = yes

## CUSTOM ACTIONS (OpenRC / runit)

**extra-command**=*name* *program* [*args*...]
:   Define a custom action (`slinitctl action *svc* *name*`) callable
    in any state.

**extra-started-command**=*name* *program* [*args*...]
:   Like **extra-command**, but only callable when the service is
    *started*.

**control-command-***SIG*=*program* [*args*...]
:   Custom handler invoked when **slinitctl signal** *SIG* is called.
    Replaces the default `kill -SIG`.

## VTTY (sunlight-os)

**vtty**=*yes*|*no*
:   Reserve a virtual TTY for this service.

**vtty-scrollback**=*N*
:   Scrollback buffer size (lines).

## PRE-START GUARDS (OpenRC)

**required-files**=*path* [*path*...], **required-dirs**=*path* [*path*...]
:   Existence checks performed before exec; the service fails
    immediately if any path is missing.

## START PREDICATES (systemd-style)

Each predicate is checked *before* **required-files**/**required-dirs**
and before fork/exec. Predicates come in two flavours:

- **condition-**\* — on failure the start is *skipped silently*: the
  service transitions to STARTED with no process running. Dependents
  see a satisfied dep and proceed; nothing is logged as an error.
  Equivalent to systemd's *Condition\** directives.
- **assert-**\* — on failure the start is *aborted* and propagates to
  hard dependents like any other failed start. Equivalent to
  systemd's *Assert\** directives.

Negate any predicate by prefixing the value with `!` (whitespace
between the bang and the value is tolerated).

Recognised predicates (each has both a `condition-` and an `assert-`
form):

**\*-path-exists**=*path*
:   *path* exists (any file type, symlinks followed).

**\*-path-exists-glob**=*pattern*
:   *pattern* matches at least one filesystem entry.

**\*-path-is-directory**=*path*
:   *path* exists and is a directory.

**\*-path-is-mount-point**=*path*
:   *path* is a filesystem mount point (its device id differs from its
    parent's).

**\*-file-not-empty**=*path*
:   *path* is a regular file with non-zero size.

**\*-directory-not-empty**=*path*
:   *path* is a directory with at least one entry.

**\*-kernel-command-line**=*token*
:   `/proc/cmdline` contains *token*. *token* may be a bare key
    (`quiet`) or a `key=value` pair.

**\*-virtualization**=[*kind*|`yes`|`no`]
:   Detect the running virtualization (probe order: `/proc/1/cgroup`,
    `$container` env var, cpuinfo `hypervisor` flag, DMI strings,
    WSL fingerprint). Specific kinds: `kvm`, `qemu`, `vmware`,
    `virtualbox`, `microsoft`, `xen`, `wsl`, `docker`, `lxc`,
    `podman`, `kubernetes`. `yes` / empty matches any virt; `no`
    matches bare metal.

**\*-first-boot**[=`yes`|`no`]
:   `/etc/machine-id` is missing or `uninitialized`. Defaults to `yes`.

**\*-host**=*hostname*
:   System hostname matches *hostname* (case-insensitive).

**\*-security**=*lsm*
:   Named LSM is active. Recognised: `selinux`, `apparmor`, `tomoyo`,
    `smack`, `ima`, `audit`.

**\*-needs-update**[=`yes`|`no`]
:   `/run/systemd/update-on-next-boot` or `/run/needs-update` exists.

**\*-ac-power**[=`yes`|`no`]
:   Reads `/sys/class/power_supply` to detect AC vs battery. With no
    power-supply class entries (server / VM) the system is assumed to
    be on AC.

Examples:

    # Skip the service on first boot (silent skip, dependents proceed):
    condition-first-boot = no

    # Refuse to start outside a KVM guest:
    assert-virtualization = kvm

    # Only run when the laptop is on AC:
    condition-ac-power = yes

    # Only run if the kernel was booted with debug=1:
    condition-kernel-command-line = debug=1

## INITTAB (UTMPX)

**inittab-id**=*ID*, **inittab-line**=*tty*
:   Write a UTMPX record on start so that **who**(1) and friends see
    the session. *ID* is up to 4 characters; *tty* is the TTY name.

## PLATFORM KEYWORDS

**keyword**=[*-*]*platform* [...]
:   OpenRC-style platform gate. Prefix with `-` to skip the service
    on that platform. Recognised platforms include `docker`, `lxc`,
    `podman`, `wsl`, `xen0`, `xenu`, `prefix`, `containers`. The
    daemon's auto-detection can be overridden with **slinit**(8)
    `--sys`.

## CONSUMER / PROVIDER

**provides**=*name*
:   Register *name* as an alias for this service. Other services may
    `depends-on=`*name* and resolve to this one.

**consumer-of**=*service*
:   Mark this service as a consumer of *service*. The service file
    descriptor of *service* is passed to this service's process via
    *SLINIT_CS_FD* (and **options**=*pass-cs-fd*).

## EXAMPLE

A long-running web server that depends on the network and ships its
log through a shared logger:

    type = process
    description = nginx HTTP server
    command = /usr/sbin/nginx -g "daemon off;"
    working-dir = /

    depends-on = network
    waits-for  = mysql
    after      = filesystems

    restart = on-failure
    restart-delay = 1s
    restart-delay-step = 1s
    restart-delay-cap = 30s
    stop-timeout = 10s

    run-as = www-data:www-data
    capabilities = cap_net_bind_service
    options = no-new-privs

    cgroup = /sys/fs/cgroup/web/nginx
    cgroup-memory-high = 1G
    cgroup-cpu-weight = 200

    shared-logger = web-logger

A scripted one-shot that mounts a network share once milestones are
reached:

    type = scripted
    description = mount /srv

    depends-ms = network
    after = filesystems

    command = /usr/bin/mount /srv
    stop-command = /usr/bin/umount /srv

    required-dirs = /srv
    keyword = -docker -lxc

## SEE ALSO

**slinit**(8), **slinitctl**(8), **slinit-check**(8),
**slinit-monitor**(8).
