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

**command**=*program* [*args*...]
:   Program to execute (for **process**, **scripted**, **bgprocess**).
    Quoting and escaping follow shell-like rules.

**stop-command**=*program* [*args*...]
:   For **scripted**: program executed when the service stops.

**finish-command**=*program* [*args*...]
:   Runit-style: a program executed *after* **command** exits, before
    a possible restart.

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

slinit supports six dependency kinds. Names accept either `=` or `:`
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

**before**=*service*
:   Ordering only: if both end up starting, this one starts before
    *service*. No forced activation.

**after**=*service*
:   Ordering only: if both start, this one starts after *service*.

**chain-to**=*service*
:   When this service stops normally, automatically start *service*.

**depends-on.d**=*directory*, **depends-ms.d**=*directory*, **waits-for.d**=*directory*
:   Drop-in directories: every entry inside *directory* (regardless of
    type) is treated as a dependency of the corresponding kind.

## RESTART POLICY

**restart**=*yes*|*no*|*on-failure*
:   Whether the service is restarted automatically on exit.

**smooth-recovery**=*yes*|*no*
:   Restart in-place without notifying dependents (useful for
    short crash-restart loops).

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

**log-processor**=*program* [*args*...]
:   Pipe lines through *program* before they hit the log target.

**output-logger**=*program* [*args*...], **error-logger**=*program* [*args*...]
:   Spawn separate child processes for stdout / stderr. Implies
    **log-type=command**.

**shared-logger**=*service*
:   Send output to the named logger service (a one-to-many fan-in,
    line-prefixed with `[svc-name]`). Implies **log-type=pipe**.

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

**securebits**=*bits*
:   Securebit names or bitmask (e.g. `keep-caps,no-setuid-fixup`).

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
:   Period and initial delay.

**cron-on-error**=*continue*|*stop*
:   What to do when **cron-command** exits non-zero (default
    *continue*).

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
