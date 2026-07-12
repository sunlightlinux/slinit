# slinit 8 "" "" "slinit \- service management system"

## NAME

slinit - supervise processes and manage services (Go init system)

## SYNOPSIS

**slinit** [*OPTION*]... [*service-name*]...

## DESCRIPTION

**slinit** is a process supervisor and service manager that can also act as
the system **init** process (PID 1) on Linux. It implements the dinit base
model in Go, with additional features drawn from runit, s6-linux-init and
OpenRC. The control protocol and service-description file format are
backwards-compatible with dinit.

slinit can run in three modes:

* **System manager** (PID 1): supervises services and is responsible for
  shutdown, reboot, soft-reboot, kernel-cmdline parsing, console set-up
  and orphan reaping. Selected automatically when started as PID 1, or
  explicitly with **-m** / **\--system-mgr**.

* **System service manager**: supervises services system-wide but does
  not own machine shutdown (the primary init does). Selected with
  **-s** / **\--system**, the default when invoked as root.

* **User service manager**: supervises a per-user service tree. Selected
  with **-u** / **\--user**, the default when invoked as a non-root user.

* **Container mode**: like system-mgr but exits cleanly instead of
  rebooting/halting the machine, suitable as PID 1 inside Docker / LXC /
  Podman. Selected with **-o** / **\--container**.

Service descriptions are read from one of several directories (see
**FILES**), and only on demand: a service file is loaded the first time
its service is referenced, and is cached until **slinitctl unload** or
**slinitctl reload** is invoked. See **slinit-service**(5) for the
service-file format.

## OPTIONS

**-d** *dir*, **\--services-dir** *dir*
:   Directory containing service description files. Comma-separated for
    multiple, or repeated. When given, the built-in defaults listed in
    **FILES** are *not* searched.

**-e** *file*, **\--env-file** *file*
:   Read initial environment from *file* (one *KEY*=*VALUE* per line).
    Lines starting with `#` are comments. The special directives
    `!clear`, `!unset VAR...` and `!import VAR...` are honoured. For
    PID 1 the default is */etc/slinit/environment*.

**-p** *path*, **\--socket-path** *path*
:   Path of the control socket used by **slinitctl**(8). Default for
    system mode is */run/slinit.socket*; for user mode,
    *$XDG_RUNTIME_DIR/slinitctl* if set, otherwise *$HOME/.slinitctl*.

**-F** *fd*, **\--ready-fd** *fd*
:   File descriptor on which to write the control-socket path once
    listening. Used by parent processes to detect that slinit has come
    up and is accepting commands.

**\--restore-from-snapshot** *path*
:   Replay operator intent from a snapshot file written by a prior
    slinit instance. The snapshot records which services were
    explicitly activated, which were pinned (start or stop), which
    triggered services had been triggered, and the global environment
    set via **slinitctl setenv-global**. After the boot graph is
    activated, slinit re-applies that intent so manual state from the
    previous session is preserved.

    The intended use is **soft-reboot**: when **slinitctl shutdown
    soft-reboot** runs, slinit drops a snapshot at
    */run/slinit/soft-reboot-snapshot.json* and re-execs itself with
    this flag *and* **\--run-mode=keep** appended (the latter is
    mandatory — the default *mount* mode would stack a fresh tmpfs
    over /run and hide the snapshot before the new daemon could read
    it). Operators upgrading the slinit binary on a long-running
    system therefore keep their service activations across the
    restart.

    The snapshot file is removed once successfully consumed, so a
    later restart of slinit (e.g. for diagnostics) does not silently
    replay stale intent.

    A missing snapshot file is not an error — it is the normal case on
    a fresh boot. A snapshot whose schema **version** is newer than
    this binary understands is rejected; an older snapshot is read
    as-is (the format is additive).

    The snapshot only records *intent*, not running PIDs: services
    are re-spawned, not re-attached. For zero-downtime per service
    use a HA cluster (see **slinit-resource**(7)) instead.

**-l** *path*, **\--log-file** *path*
:   Append log messages to *path* instead of syslog. Console messages
    are still emitted unless **-q** is given. When running as PID 1 and
    the file cannot be opened (e.g. root FS still read-only), slinit
    keeps going and retries later; otherwise it exits with an error.

**-1**, **\--console-dup**
:   When **\--log-file** is also set, duplicate every log line to
    */dev/console* in addition to the file. Useful for headless boots
    where you want both a persistent log and live console output.

**-s**, **\--system**
:   Run as a system service manager. Default when invoked as root.

**-m**, **\--system-mgr**
:   Run as the system manager (i.e. own shutdown / reboot). Default
    when running as PID 1. The main observable effect is that slinit
    will execute **slinit-shutdown**(8) once all services have stopped.

**-u**, **\--user**
:   Run as a user service manager. Default for non-root invocations.

**-o**, **\--container**
:   Run in container mode. slinit will not perform machine shutdown
    on stop; it simply exits with the appropriate status. Intended for
    use as PID 1 inside Docker, LXC, Podman, etc.

**-r**, **\--auto-recovery**
:   On apparent boot failure (every service has stopped without a
    shutdown command), automatically start the **recovery** service
    rather than prompting on the console.

**-q**, **\--quiet**
:   Suppress all but error-level console output. Equivalent to
    **\--console-level error**.

**-t** *service-name*, **\--service** *service-name*
:   Start *service-name* (and its dependencies) at boot. May be
    repeated. If no service is named, the **boot** service is started.

**-b** *path*, **\--cgroup-path** *path*
:   Default cgroup base path. Relative cgroup paths in service files
    are resolved against this. Linux only.

**-a** *list*, **\--cpu-affinity** *list*
:   Default CPU affinity for the daemon and its services, e.g. `0-3`
    or `0,2,4`.

**-B**, **\--no-catch-all**
:   Disable the catch-all logger (otherwise: *services that did not
    open their own log file have their stdout/stderr captured and
    appended to* */run/slinit/catch-all.log* *or* the path given via
    **\--catch-all-log**).

**\--catch-all-log** *path*
:   Override the catch-all log file path.

**\--shutdown-grace** *duration*
:   Per-service SIGTERM→SIGKILL grace period during shutdown. Accepts
    Go duration syntax (`3s`, `5000ms`, `1m`). Default `3s`.

**\--banner** *text*
:   Boot banner printed on the console at startup. Empty disables.

**\--umask** *octal*
:   Initial umask, e.g. `0022`. Default `0022`.

**\--devtmpfs-path** *path*
:   Mount *devtmpfs* at *path* during PID-1 init (default `/dev`).
    Empty disables the mount entirely (useful when the initramfs
    has already populated */dev*).

**\--run-mode** *mode*
:   How */run* is staged at boot: `mount` (mount a fresh tmpfs),
    `remount` (re-mount the existing one with safe options) or `keep`
    (leave it as-is). Default `mount`.

**\--kcmdline-dest** *path*
:   Snapshot */proc/cmdline* to *path* during PID-1 init for later
    inspection. Default `/run/slinit/kcmdline`. Empty disables.

**\--timestamp-format** *fmt*
:   Log timestamp format: `wallclock`, `iso`, `tai64n`, or `none`.
    Default `wallclock`.

**\--no-wall**
:   Suppress wall(1)-style broadcasts to logged-in users at shutdown.

**\--rlimits** *spec*
:   Default resource limits for services that do not override them.
    See **slinit-service**(5) for the syntax (`RES=soft:hard,...`).

**\--parallel-start-limit** *N*
:   Maximum concurrent service starts (`0` = unlimited, the default).
    Useful on slow IO substrates to throttle parallel boot.

**\--parallel-start-slow-threshold** *duration*
:   How long a service must remain in the *starting* state before it
    is reported as slow. Default `10s`.

**-S** *kind*, **\--sys** *kind*
:   Override platform auto-detection. Accepted values match the set
    detected by **pkg/platform**: `docker`, `lxc`, `podman`,
    `systemd-nspawn`, `openvz`, `vserver`, `rkt`, `uml`, `wsl`,
    `xen0`, `xenu`, `kvm`, `qemu`, `vmware`, `microsoft` (Hyper-V),
    `oracle` (VirtualBox), `bochs`, or `none`. Mostly useful for
    testing container-mode / VM-specific behaviour outside a real
    environment.

**\--conf-dir** *dirs*
:   Override the default conf.d overlay directories
    (*/etc/slinit.conf.d* in system mode). Comma-separated; the
    literal `none` disables overlays entirely.

**\--watch-services-dir**
:   Opt-in: watch every **\--services-dir** with **inotify**(7) and
    auto-load a service when a new file appears (or is renamed in),
    auto-unload it when the file is removed. Modified files are
    logged; slinit's existing *(modified since loaded)* marker still
    surfaces the change via `slinitctl status`. Services are loaded
    but **not** auto-started (matches dinit's explicit-start model);
    the operator can then `slinitctl start <name>`. Unload happens
    only when the service is *STOPPED* — running services are left
    loaded with a warning. Editor artefacts (dotfiles, `~`, `.swp`,
    `.tmp`, `.new`, `.bak`) and `.d` overlay dirs are ignored. A
    300 ms debounce window collapses rapid multi-event bursts (write
    + close + rename) into a single dispatch per file. Inspired by
    **runsvdir**(8)'s inotify rescan (runit 2.3.1+).

**\--stderr-ring-buffer-size** *bytes*, **\--stderr-ring-buffer-interval** *duration*
:   Opt-in: capture the daemon's own recent log output in an
    N-byte in-memory ring buffer and re-emit its contents on stderr
    every *duration* (default 15m). Inspired by **runsvdir**(8)'s
    optional rolling-buffer second argument. Useful when transient
    warnings would otherwise scroll past unnoticed — the buffer
    guarantees each captured message stays visible until at least
    one dump has emitted it. 0 (default) disables the feature; no
    ring is allocated and the logger stays zero-overhead. Minimum
    accepted size is 16 bytes; values below that are silently
    promoted. The buffer is cleared after each dump so a quiet
    period between ticks produces no output rather than repeating
    the previous dump.

**\--heartbeat-interval** *duration*, **\--heartbeat-restart-window** *duration*
:   Opt-in: emit a single grep-friendly summary line at each
    *interval* with the supervisor's own health signals. Fields:
    **active**, **failed**, **stopped**, **starting**, **stopping**
    service counts; **restarts(N)** count over the sliding
    *restart-window* (default 1m); **watchdog-misses** cumulative
    counter; **rss** in kilobytes read from /proc/self/status.
    0 (default) disables. Useful as a lightweight SLI feed for
    monitoring systems that don't need to open the control socket
    to check whether the supervisor is healthy.

**\--active-profile** *name*
:   Activate profile *name* at boot (runit *runsvchdir* analogue).
    Services declaring **profile = *name*** (or **profile = ...,
    *name*, ...** — see **slinit-service**(5)) become eligible
    for the boot-service auto-start pass; services tagged with
    other profiles are loaded but not started. Services with no
    profile tag ("global infrastructure") are always eligible
    regardless. The active profile can also be switched at
    runtime via **slinitctl activate-profile**. Empty (default)
    means no filter — every boot service starts as normal.

**\--sentinel-dir** *dir*
:   Opt-in: watch *dir* with **inotify**(7) for runit-compatible
    sentinel files that, when armed with **+x**, drive slinit's own
    shutdown surface out-of-band from the control socket. Recognized
    filenames: **stopit** (halt), **reboot**, **poweroff**. A file
    without the executable bit is treated as staged-but-unarmed —
    the operator can prepare it in advance and flip `chmod +x` when
    the trigger should fire (this matches **runit**(8)'s workflow).
    Every trigger logs an audit line with the file owner's UID and
    mtime before the file is unlinked, so a compliance regime that
    requires forensic evidence of who requested a system state
    change gets a durable filesystem-anchored record. Pre-existing
    armed files are honoured at boot via an initial scan, so an
    admin who dropped **reboot** while slinit was down still gets
    the reboot when it comes back up. Empty (default) disables the
    watcher — the vast majority of installations don't need this,
    and the socket + signal surface already covers the common cases.
    Intended for database servers, telco control planes, and other
    workloads where the audit trail matters as much as the trigger.

**\--emergency-timeout** *duration*
:   Maximum time slinit waits for services to drain during shutdown
    before flipping into the force-exit path (SIGKILL to any straggler,
    then hand off to the reboot/halt/kexec syscall). Default *90s*.
    Zero (the flag's zero-value on daemon start) falls through to the
    default; negatives are treated the same. When the timer fires the
    error log line names every still-blocking service in-line
    (**"Services did not stop within Xs, forcing shutdown; still
    blocking: docker (STOPPING, pid 1234), elogind (STOPPING, pid
    5678)"**) so the operator doesn't have to correlate with the
    periodic reporter that was scrolling past. Workloads with a heavy
    stop cascade (docker + dbus + full systemd-style service graph)
    can safely tune this up to **3m** or **5m**.

**\--log-level** *level*
:   Minimum level for the main log facility (file or syslog). One of
    `debug`, `info`, `notice`, `warn`, `error`. Default `info`.

**\--console-level** *level*
:   Minimum level for the console. Defaults to **\--log-level**;
    overriding lets you keep a chatty file log while the console is
    quieter (or vice-versa).

**\--watchdog-device** *path*
:   Hardware watchdog character device to feed. When empty (the
    default) slinit auto-discovers */dev/watchdog0*, falling back to
    */dev/watchdog* on systems that only expose the legacy alias.
    Only applied when slinit runs as PID 1 or in container mode.

**\--watchdog-timeout** *duration*
:   Kernel-side timeout programmed via *WDIOC_SETTIMEOUT*. The
    kernel rounds to the nearest value its driver supports.
    Default *60s*. Accepts any **time.ParseDuration** form (*30s*,
    *2m*).

**\--watchdog-interval** *duration*
:   How often the feeder pings the device. Defaults to
    *timeout / 3* — three pings per timeout window survive a single
    dropped tick (e.g. brief CPU starvation) before the kernel
    resets the box.

**\--no-watchdog**
:   Disable the hardware-watchdog feeder even when running as PID 1
    with a watchdog device present. Useful for development VMs and
    test rigs where a stuck slinit must NOT trigger a hardware
    reset.

**\--version**
:   Print the slinit version and exit.

**\--help**
:   Print a brief help text and exit.

## SPECIAL SERVICE NAMES

**boot**
:   Started by default if no service is named on the command line.
    Conventionally a *type=internal* service that depends on every
    service the system needs at boot.

**recovery**
:   When **\--auto-recovery** is set or the operator selects it from
    the recovery prompt, slinit starts this service instead of
    declaring a boot failure. Typically launches a single-user shell.

**runlevel-N** / **runlevel-***name*
:   When invoked as `init N` (SysV-style) slinit starts
    *runlevel-N* if it exists. OpenRC-style names (`single`,
    `nonetwork`, `default`, `boot`, `sysinit`) dispatch the same way.
    These are pure aliases — slinit has no native runlevel concept.

## SERVICE ACTIVATION MODEL

slinit maintains a set of running services. Each service is either
*explicitly activated* (started via the boot command line, an
**slinitctl start**, or a SysV-init runlevel switch) or *implicitly
activated* (it is a dependency of an active service).

Hard dependencies must succeed before a dependent will start; if any
hard dependency fails, the dependent will not be started either. Soft
dependencies (waits-for, waits-for.d, depends-ms) influence start
ordering and stop ordering but never block startup. **slinitctl
release** removes explicit activation, stopping the service iff it has
no remaining active dependents. **slinitctl stop** also removes
explicit activation, but additionally fails if other services still
depend on the target.

## RUNNING AS SYSTEM MANAGER / PID 1

When started as PID 1, slinit performs early init before opening the
control socket: console set-up, kernel-cmdline parsing,
*/proc*/*/sys*/*/run* / *devtmpfs* mounting, signal handling
(SIGINT → reboot, SIGTERM → halt, SIGQUIT → immediate shutdown),
subreaper / orphan reaping, control-alt-delete handling, and a
boot-time clock guard (a compile-time floor plus a persistent
timestamp file at */var/lib/slinit/clock*) to avoid running with a
silently-reset RTC.

In container mode (**-o**) the same supervision logic runs but
shutdown is replaced with a clean process exit, leaving teardown to
the container runtime.

## LOGGING

slinit logs to two facilities: the *console* (standard output, which
may be a real console or a redirected file) and the *main log* (the
syslog facility by default, or a file if **\--log-file** is given).
Log levels, lowest to highest: **debug**, **info**, **notice**,
**warn**, **error**. **none** silences a facility entirely.

Service-state messages (started, stopped, failed) are notice-level for
success, error/warn for failure (warn for transitive failures caused
by a dependency). With **-q** they are suppressed; with
**\--console-level** you can re-enable just one severity.

## KERNEL COMMAND LINE

When running as PID 1 on Linux, kernel-cmdline tokens not consumed by
the kernel are passed to slinit as argv. To avoid surprising
interactions with bootloader options like `auto`, slinit only treats
"naked" tokens as service names when they are recognised (**single**
maps to a *runlevel-single* service, if defined). Tokens prefixed
with `--` are normal long options. Tokens of the form *KEY*=*VALUE*
that are not slinit options are exported into the service environment
instead of being processed as arguments.

To force a specific service name regardless, prefix it with **-t**
(or **\--service**) — that form is always honoured.

## SIGNALS

When running as system manager (PID 1 or **-m**):

* *SIGINT* — reboot (also generated by control-alt-delete on Linux)
* *SIGTERM* — halt
* *SIGQUIT* — immediate shutdown, no service rollback
* *SIGUSR1* — re-open the control socket if it has been deleted

When running as a user or system service manager:

* *SIGINT* / *SIGTERM* — stop services and exit
* *SIGQUIT* — exit immediately
* *SIGUSR1* — re-open the control socket

## ENVIRONMENT

The following environment variables are exported into the start
environment of every service spawned by slinit:

* **SLINIT_SERVICENAME** — the service's name as known to slinit
* **SLINIT_SERVICEDSCDIR** — the directory the service file was
  loaded from (set only when not synthesised)

The boot environment may be set up via **\--env-file**, *!*-prefixed
directives in that file, or *KEY*=*VALUE* tokens on the kernel
command line.

## FILES

*/etc/slinit.d*, */run/slinit.d*, */usr/local/lib/slinit.d*, */lib/slinit.d*
:   Default service description directories for system mode. Searched
    in this order; the first match wins.

*$XDG_CONFIG_HOME/slinit.d*, *~/.config/slinit.d*, */etc/slinit.d/user*, */usr/lib/slinit.d/user*, */usr/local/lib/slinit.d/user*
:   Default service description directories for user mode.

*/etc/slinit.conf.d*
:   Default conf.d overlay directory. Files dropped here override
    matching settings on top of any service description.

*/etc/slinit/environment*
:   Default environment file for system mode.

*/etc/slinit/shutdown.allow*
:   When present, controls which users may invoke **slinit-shutdown**
    in delegated mode.

*/run/slinit.socket*
:   Default control socket for system mode.

*/var/lib/slinit/clock*
:   Persistent timestamp updated periodically and at shutdown; used
    on the next boot as a clock floor when the RTC has reset.

*/run/slinit/catch-all.log*
:   Default catch-all log: stdout/stderr from services that did not
    redirect their own output.

*/run/slinit/kcmdline*
:   Snapshot of */proc/cmdline* taken during PID-1 init.

*/run/slinit/soft-reboot-snapshot.json*
:   Operator-intent snapshot written just before a soft reboot and
    consumed by the re-execed slinit via **\--restore-from-snapshot**.
    Lives on tmpfs so it does not survive a real reboot.

*/dev/watchdog0*, */dev/watchdog*
:   Hardware-watchdog character devices fed by slinit when running as
    PID 1 (see **\--watchdog-device**, **\--no-watchdog**). The kernel
    timer is disarmed via the magic-close byte (`V`) before any
    orderly shutdown so the reboot path is never truncated by a
    watchdog reset.

## EXIT STATUS

When run as a non-PID-1 service manager, slinit exits 0 on a clean
shutdown. As PID 1 it normally does not exit; on error before init it
exits with a non-zero status.

In container mode, the exit status reflects the shutdown reason
(*0* for **slinitctl shutdown**, *1* if forced).

## SEE ALSO

**slinitctl**(8), **slinit-service**(5), **slinit-check**(8),
**slinit-monitor**(8), **slinit-shutdown**(8).

## AUTHORS

slinit is a Go reimplementation of dinit (originally written by Davin
McCall) with features ported from runit, s6-linux-init and OpenRC.
Maintained by the sunlight-os project.
