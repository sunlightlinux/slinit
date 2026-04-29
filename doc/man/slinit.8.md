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
:   Override platform auto-detection. Accepted: `docker`, `lxc`,
    `podman`, `wsl`, `xen0`, `xenu`, `none`. Mostly useful for testing
    container-mode behaviour outside a real container.

**\--conf-dir** *dirs*
:   Override the default conf.d overlay directories
    (*/etc/slinit.conf.d* in system mode). Comma-separated; the
    literal `none` disables overlays entirely.

**\--log-level** *level*
:   Minimum level for the main log facility (file or syslog). One of
    `debug`, `info`, `notice`, `warn`, `error`. Default `info`.

**\--console-level** *level*
:   Minimum level for the console. Defaults to **\--log-level**;
    overriding lets you keep a chatty file log while the console is
    quieter (or vice-versa).

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
