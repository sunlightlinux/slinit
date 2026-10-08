% SLINITCTL(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-07-20

## NAME

slinitctl - control client for the slinit service manager

## SYNOPSIS

**slinitctl** [*global-options*] *command* [*command-options*] [*service-name*]

## DESCRIPTION

**slinitctl** is the command-line client for **slinit**(8). It connects to
the slinit control socket and issues a single command per invocation
(start, stop, query, configure, …). Replies are printed to stdout; errors
to stderr with a non-zero exit status.

A few commands (currently **enable**, **disable**) accept the **\--offline**
flag and operate directly on service files without contacting a running
daemon, which is useful at install time or in initramfs.

## GLOBAL OPTIONS

Global options go before the command: `slinitctl --force stop db`, not
`slinitctl stop --force db`. Parsing stops at the first word that is not
one of them, and that word is the command; anything after it is the
command's own arguments, so a global option placed there is taken as a
service name or rejected.

**-p** *path*, **\--socket-path** *path*, **\--socket-path=***path*
:   Path to the slinit control socket. Without it, *$DINIT_SOCKET_PATH*
    is used, then *$SLINIT_SOCKET_PATH*; failing both, the default is
    */run/slinit.socket* in system mode and *$XDG_RUNTIME_DIR/slinitctl*
    (or *$HOME/.slinitctl* when *XDG_RUNTIME_DIR* is unset) in user
    mode.

**-s**, **\--system**
:   Connect to the system service manager.

**-u**, **\--user**
:   Connect to the user service manager. This is the default for
    non-root users; root gets the system manager unless **-u** is
    given.

**-q**, **\--quiet**
:   Suppress informational output.

**\--no-wait**
:   For **start**, return as soon as the daemon has accepted the
    request instead of waiting for the service to reach **STARTED** or
    fail. Also implies **\--quiet**.

**-w** *SEC*, **\--wait** *SEC*, **\--wait=***SEC*
:   Fail with a timeout error if the daemon does not reply within
    *SEC* seconds. 0 (default) disables the CLI-side cap — the
    daemon's own start/stop/reload timeouts still apply. Mirrors
    **sv**(8) `-w SEC`: useful when scripting bulk operations against
    a daemon that might be pathologically slow, so the script can
    move on rather than block indefinitely. Note that a timeout only
    stops the CLI from waiting; the underlying operation may still
    complete server-side.

**\--pin**
:   For **start** and **stop**: pin the service in the requested state
    so that automatic restart / dependency-driven stop cannot move it.
    For **restart**, the pin is applied by the start half. Use
    **unpin** to clear.

**-f**, **\--force**
:   For **stop** and **restart**: stop the service even when it is
    pinned started or declares **refuse-manual-stop**. Dependents are
    force-stopped along with it, pins and all.

**\--ignore-unstarted**
:   Accepted for **dinitctl**(8) compatibility. It changes nothing:
    **stop** and **restart** already succeed (exit 0, with an
    "already stopped" note unless **\--quiet**) when the service is
    not running.

**-o**, **\--offline**
:   For **enable** / **disable**: work directly on the service files
    without talking to a daemon.

**-d** *dir*, **\--services-dir** *dir*, **\--services-dir=***dir*
:   Service directory used by **\--offline** mode. Defaults to
    */etc/slinit.d* with **\--system**, and otherwise — root included —
    to *$XDG_CONFIG_HOME/slinit.d* or *$HOME/.config/slinit.d*. A
    relative path is resolved against the current directory.

**\--from** *service*, **\--from=***service*
:   For **enable** / **disable**: name of the *source* service the
    **waits-for** edge hangs from. Without it, the daemon uses the
    target's **@meta enable-via**, then the boot service (**boot**
    with **\--offline**).

**\--use-passed-cfd**
:   Take the control-socket file descriptor from the environment
    variable *SLINIT_CS_FD* instead of opening one. slinit sets it
    (and *DINIT_CS_FD*) for services declaring **options = pass-cs-fd**.

**\--dinit-compat**
:   For **disable**: use the dinit-compatible request (remove the
    dependency, then delete the symlink client-side) instead of the
    slinit-native single request. Needed only when talking to a real
    **dinit** daemon.

**-h**, **\--help**
:   Show usage and exit.

**\--version**
:   Show version and exit.

## COMMANDS

### Service lifecycle

**start** *service*
:   Activate *service*. Starts dependencies as needed.

    Waits until *service* reaches a terminal state and exits non-zero
    if it did not come up, so `slinitctl start foo && ...` is safe to
    write. Pass **\--no-wait** to return as soon as the daemon has
    accepted the request.

    A **triggered** service is the one case where waiting is usually
    wrong: it stays in STARTING by design until **trigger** fires, so
    `slinitctl start` on one blocks until something else triggers it.
    Use **\--no-wait** there.

**wake** *service*
:   Start *service* without marking it explicitly active, on behalf of
    its dependents: it succeeds only if at least one service that
    depends on it is started or starting, and the service then stays up
    only as long as such a dependent needs it. Refused for a
    stop-pinned service, and for a **manual** service that has not been
    started explicitly. Used to "rejoin" a previously released service.

**stop** *service*
:   Stop *service*. Services with a hard dependency on it are stopped
    too; **waits-for** dependents lose the link and keep running.
    Refused for a service pinned started or declaring
    **refuse-manual-stop**, unless **\--force** is given. Unlike
    **start**, the command returns once the daemon has accepted the
    request; it does not wait for **STOPPED**.

**release** *service*
:   Remove explicit activation from *service*. Stops it iff no other
    active service still requires it.

**restart** *service*
:   Stop and then start *service*: two requests sent back to back,
    without waiting for the outcome of either.

**signal** [**-l** | **\--list**] *signal* *service*
:   Send *signal* to the service's main process. *signal* may be a
    name (`HUP`, `TERM`, `USR1`, …) or a number. **-l** lists the
    accepted signal names.

**pause** *service*
:   Send SIGSTOP to the service's process group (only the main process
    with **options = signal-process-only**), or run its
    **control-command-STOP** if it has one. The service remains in the
    *running* state from slinit's point of view.

**continue** *service* (alias **cont**)
:   Counterpart to **pause**: send SIGCONT.

**freeze** *service*
:   Freeze every process in the service's cgroup via the cgroup v2
    *cgroup.freeze* interface. Unlike **pause** (which sends
    SIGSTOP and can be observed / bypassed by the target), a
    frozen cgroup is opaque to the frozen processes — they are
    suspended by the kernel and cannot receive signals other than
    SIGKILL. Requires a configured **cgroup** for the service and
    cgroup v2. The service stays in *running* from slinit's
    perspective.

**thaw** *service*
:   Counterpart to **freeze**: clear *cgroup.freeze*.

**once** *service*
:   Like **start**, but set the service's restart policy to never
    first, so it is not restarted when it exits — a one-shot-style
    execution. The policy is not restored afterwards: it stays at
    never for later starts too, until the description is reloaded
    (**reload**, **reload-all**).

**unpin** *service*
:   Clear a previous **\--pin** on *service*.

**run** [*flags*] **\--** *COMMAND* [*ARGS*...]
:   Spawn a transient one-shot service without writing a service
    file (systemd-run analogue). The description is written to
    */run/slinit.d/\<name\>* (tmpfs, evaporates at boot) and the
    service is loaded + started via the standard code path — no
    protocol change needed.

    Flags:

    - **\--unit** *NAME* — transient unit name (default
      *run-\<rand-hex\>*).
    - **\--description** *STR*
    - **\--type** *process*|*scripted* (default *process*)
    - **\--slice** *NAME* — cgroup slice.
    - **\--nice** *N* — nice value (int).
    - **\--run-as** *USER*[:*GROUP*] — credential drop via
      **run-as =**.
    - **\--on-active** *DURATION* — one-shot timer form: sleep
      *DURATION* before exec'ing the target. Wraps the argv in
      */bin/sh -c 'sleep N; exec …'*. Accepts *5s* / *200ms* /
      *1h* / bare-integer-seconds. systemd-run's
      **\--on-active**.
    - **\--setenv** *VAR*=*VAL* — repeatable; written to a
      companion *\<name\>.env* file and referenced via
      **env-file =**.
    - **\--property** *KEY*=*VAL* — repeatable pass-through of
      any slinit config directive (KEY is slinit-native kebab-
      case).
    - **\--wait** — block until STARTED (or STOPPED for scripted).
      60s cap.
    - **\--collect** — block until STOPPED, then unload + remove
      the transient description + .env sidecar. No cap; Ctrl-C
      is the escape hatch.

    The description is always written to */run/slinit.d*, which
    requires write access there and is among the default service
    directories of a system instance only. **\--user** targets the
    user socket, but a user instance finds the transient service only
    if */run/slinit.d* is one of its **\--services-dir** entries.

    The transient service is started without waiting; **\--wait**
    and **\--collect** then poll its state. A failed start removes
    the description again.

### Status & queries

**list** (alias **ls**)
:   List all loaded services and their state (started / stopped /
    starting / stopping / failed).

**status** [**-l** | **\--long** | **\--full**] *service*
:   Print a multi-line status block for *service*. Human-oriented: the
    wording, colours and alignment are free to change between releases.
    Use **show** for anything a script reads. **-l** appends a
    *Details:* block with the service's full configuration.

**show** [**-l**] *service*
:   Dump the service's full configuration and live state as
    *Key=Value* lines, one per line, in the manner of **systemctl
    show**. Fields are grouped semantically — identity, state,
    timestamps, restart, kill, cgroup, security, dependencies, exec —
    rather than alphabetically, so a naked **show** reads top to
    bottom like a unit dump. Unset optional fields are omitted.

    This is the machine-readable surface: parse it with `awk -F=` or
    `grep '^Key='`, by key and never by line number. Keys are not
    removed or renamed within a major version, and new ones may
    appear — see **STABILITY.md**.

    **-l** / **\--long** is accepted and does nothing, since **show**
    already renders every field. It warns on stderr rather than
    swallowing the flag silently, so a scripter reaching for it out of
    **systemctl** habit notices and drops it.

**is-started** *service*
:   Print the service's state and exit 0 iff it is **STARTED**, 1
    otherwise. The state is printed even with **\--quiet**; redirect
    stdout when only the exit status matters.

**is-failed** *service*
:   Exit 0 iff *service* failed: its last start failed, or it is
    stopped with a non-zero exit status. Prints *FAILED* in that case
    and the state otherwise, even with **\--quiet**.

**reset-failed** [*service*]
:   Clear the internal *start failed* mark, which **is-failed** and
    **status** report. The restart-limit counter is not touched. With
    no argument, clears the mark on every loaded service. Mirrors
    systemd's **reset-failed** subcommand.

**dependents** *service*
:   Print the services that have a dependency of any type on
    *service*.

**query-name** *service*
:   Load *service* and print the canonical name the daemon knows it
    under — for example the name a **provides** alias resolves to.

**service-dirs**
:   Print the list of service directories the daemon is searching.

**query-load-mech** (alias **load-mech**)
:   Print the daemon's load mechanism (which is currently always
    *file*; reserved for future load backends).

**boot-time** (alias **analyze** [*subcommand*])
:   Print boot-time analysis: kernel→userspace handoff, slinit
    startup, per-service start times, slow services. As **analyze** it
    takes an optional subcommand:

    - *time* / *blame* / omitted — the analysis described here.
    - *critical-chain* [*service*] — the slowest dependency sequence
      leading to *service* (or to the boot target), which is the one
      worth attacking when a boot is slow.
    - *dot* — the dependency graph as Graphviz DOT, identical to
      **graph**; pipe it through `dot -Tsvg`.
    - *plot* (since 2.4.8) — the boot as an SVG timeline on stdout, one
      lane per service, the equivalent of `systemd-analyze plot`:
      `slinitctl analyze plot > boot.svg`. A lane runs from the moment
      the service was asked to start to the moment it reported started,
      so time spent *waiting on a dependency* is part of the bar — the
      same span **blame** reports as a single number. A service still
      starting gets a dashed open-ended bar, which is what makes the
      plot worth taking during a boot that is hanging.

      Only the boot window is plotted. A service whose start was
      requested after the boot target came up is an operator action or
      a restart, not part of the boot, and on a machine with weeks of
      uptime it would stretch the axis until the boot was one pixel
      wide; those are counted in the header instead. While the boot is
      still in progress there is no such cutoff and everything is
      drawn. The kernel gets a lane of its own when its figure adjoins
      slinit's start and is in proportion to it — not after a soft
      reboot, where the figure is carried from an older boot, and not
      under **\--user**, where it is the machine's uptime. In those
      cases the number moves to the header and the timeline starts
      where slinit did.

    After a soft reboot the kernel figure is the one from the original
    boot, carried forward in the soft-reboot snapshot — the kernel did
    not restart, so this generation cannot measure it. An extra line
    reports which generation this is and how far into the machine's
    uptime it started. A snapshot written by a slinit older than 2.3.9
    carries no kernel figure, and the output says so rather than
    substituting the uptime.

**catlog** [**\--clear**] *service*
:   Print *service*'s in-memory log buffer. **\--clear** truncates the
    buffer after printing.

**graph**
:   Print the dependency graph of every loaded service as Graphviz DOT.
    Arguments are ignored: the full graph is always printed.

**list5**, **status5** *service*
:   Same output as **list** / **status** but using the v5 wire
    protocol, which adds *stop_reason*, *exec_stage* and *si_code* /
    *si_status* fields. Useful for debugging service exits.

    How to read the exit fields. *Exit* is the process's own code, and
    **0 is printed** — its absence means no exit has been recorded
    (running, or killed by a signal, in which case *si_status* names the
    signal). *Exec-stage* and *Exec-errno* appear instead of *si_code* /
    *si_status* when the process never ran. The daemon now says so in the
    status flags (since 2.7.2) instead of leaving the client to guess
    from the stage number — stage 0 is a real stage, so a failure while
    arranging file descriptors used to print its errno as though it were
    an si_code. A stop reason of *exec-failed* is accepted as the same
    signal, which is how dinit reports it, so an older daemon still reads
    correctly.

**attach** *service*
:   Connect the terminal to the service's virtual TTY (a service with
    **vtty = yes**), screen-style: output is shown and keystrokes are
    forwarded. Press **Ctrl+]** to detach; the service keeps running.
    Works without the control socket: with **\--system** it connects
    to */run/slinit/vtty-*\ *service*\ *.sock*, otherwise to the same
    name under *$HOME/.slinit/* — so pass **\--system** for a system
    service even when running as root.

### Configuration & environment

**reload** *service*
:   Re-read *service*'s description from disk. Refused while the
    service is **STARTING** or **STOPPING**. A **STOPPED** service is
    replaced outright, type changes included. A **STARTED** service is
    updated in place — the running process is not touched, and a new
    command or similar takes effect on the next start — and the reload
    is refused if it would change the type, the console flags, the log
    type or (for **bgprocess**) the pid-file, or add a hard dependency
    that is not already **STARTED**.

**reload-all**
:   Re-read every loaded service description from disk in one round
    trip. Services in transitional states (**STARTING** / **STOPPING**)
    are skipped silently — operators retry once the service settles.
    Prints a summary like "Reloaded 12 service(s)." on success or
    "Reloaded 11 service(s); 1 failed (see daemon log)." with a
    non-zero exit when one or more reloads were rejected. The
    per-service rules of **reload** apply. Typical use:
    ops applied a config update across many service files and want
    them all picked up without scripting a `for` loop.

**start-all**
:   Start every loaded service that is not already **STARTED**, in one
    round trip. A recovery command: after processes have been killed out
    from under the daemon, or a batch of services was stopped by hand,
    it brings the set back up without scripting a loop over
    **slinitctl ls**.

    Prints a summary like "Starting 7 service(s); 35 already running or
    skipped". It says *starting*, not *started*: the requests are
    issued, not awaited, so a service can still fail afterwards — check
    **slinitctl ls** or the log. There is deliberately no failure count,
    since a synchronous one would always read zero.

    Four kinds of service are passed over rather than started:
    those in a transitional state (**STARTING** / **STOPPING**, same
    reasoning as **reload-all**); those declaring **manual = yes**,
    which is documented as refusing every activation path except an
    explicit **slinitctl start** of that service, and a bulk sweep is
    not that; those declaring **refuse-manual-start**, since the bulk
    path has no business being more permissive than the per-service one;
    and stop-pinned services, because a pin is recorded operator intent
    and outranks a sweep.

    slinit-native — dinit has no bulk-start equivalent. To bring a
    machine all the way back to its post-boot state instead, including
    the dependency-only activation markers that **start-all** cannot
    reproduce (it marks what it starts active), use
    **slinitctl shutdown softreboot**.

**activate-profile** *name* | **-**
:   Swap the active profile (runit *runsvchdir* analogue).
    Services declaring **profile = *name*** (see
    **slinit-service**(5)) that are not currently in the outgoing
    profile are started; services in the outgoing profile that
    are not in the incoming one are stopped. Services with no
    profile tag ("global") are always kept. Reports the
    started / stopped / kept service lists on success. Passing
    **-** as the name deactivates the filter without stopping
    anything — every service becomes eligible again on the next
    load or boot pass. Rejected with a NAK if no loaded service
    declares the requested name, so a typo does not silently
    stop every profile-tagged service.

**active-profile**
:   Prints the name of the currently active profile. Prints
    "(no active profile)" and exits 0 when the daemon started
    without **\--active-profile** and no runtime activation
    has occurred.

**list-profiles**
:   Enumerates every profile tag declared by any currently
    loaded service, sorted alphabetically. Empty output means
    no loaded service uses the **profile** stanza.

**reload-signal** *service*
:   Send the signal declared in *service*'s **reload-signal** stanza
    (see **slinit-service**(5)) to its main running process. This is
    the "tell the daemon to re-read its own config" idiom — typical
    for nginx, sshd, syslog and similar services. Rejects the
    request when *service* has no **reload-signal** configured
    (with an explicit error rather than silently no-op'ing) or when
    the service has no running process. Different from **reload**
    above, which re-reads the slinit-side service description and
    does not touch the running process.

**unload** *service*
:   Drop *service* from the in-memory set. Only allowed when the
    service is stopped, no loaded service depends on it other than
    through **before**/**after** ordering, and it is not consuming
    another service's log output.

**add-dep** *from* *kind* *to*, **add-dep** *kind* *from* *to*
:   Add a dependency edge of *kind* (`depends-on`/`regular`,
    `waits-for`/`soft`, `depends-ms`/`milestone`, `prepared-by`,
    `before`, `after`) from *from* to *to*. `prepared-by` behaves as
    a hard dependency that also cascades a restart from *from* back to
    *to* — see **slinit-service**(5).

    **Both argument orders work** (since 3.0.0). slinitctl has always
    taken *from* *kind* *to*; **dinitctl**(8) takes *kind* *from* *to*,
    and this page documented dinit's order while the implementation used
    its own. Rather than break whichever set of scripts was following the
    other, the type names are a closed set, so whichever position holds
    one of them is the *kind*. A service named after a dependency type is
    the one ambiguous case: the middle position wins, which is the order
    this command has always implemented.

**rm-dep** *from* *kind* *to*, **rm-dep** *kind* *from* *to*
:   Remove a dependency edge of *kind*. Both orders, as for **add-dep**.

[**\--from** *src*] **enable** *service*
:   Add a **waits-for** edge from *src* (default: the service's
    **@meta enable-via**, else the boot service) to *service*, persist
    it as a symlink, and start *service* — the equivalent of
    **systemctl enable --now**. The symlink goes into the first
    **waits-for.d** directory *src* declares, resolved against *src*'s
    service directory; when *src* declares none, into
    *waits-for.d/* beside *src*'s file, which the loader does not
    scan, so the edge then lasts only until the daemon restarts.

    With **\--offline**, nothing is started: the service files in *dir*
    are read to find the same directory, and the symlink is written
    there. Since the daemon's fallback directory would never be read,
    an offline **enable** of a *src* that declares no **waits-for.d**
    is refused with an error instead.

[**\--from** *src*] **disable** *service*
:   Inverse of **enable**: remove the edge and the symlink, and stop
    *service*. With **\--offline**, only the symlink is removed, from
    the directory **enable** would use (a missing link is reported,
    not an error).

**setenv** *service* *KEY*=*VALUE*
:   Set an environment variable on *service*; it applies from the
    service's next start.

**unsetenv** *service* *KEY*
:   Remove a variable previously set with **setenv**.

**getallenv** *service*
:   Print the variables set on *service* at runtime (one *KEY*=*VALUE*
    per line).

**reset-env** *service*
:   Clear all runtime **setenv** mutations on *service*. After reset,
    the next start sees only the daemon's global environment plus the
    service's *env-file* — i.e. the defaults the service was loaded
    with. Mirrors upstart's *initctl reset-env JOB*. Operates on the
    in-memory state only and does not touch files on disk.

**setenv-global** / **unsetenv-global** / **getallenv-global**
:   Same as the **setenv** family but operate on the global
    environment (handle 0): the values are inherited by every service
    rather than installed on a single one.

**trigger** *service*
:   Set the trigger on a *type = triggered* service. It does not start
    the service: a triggered service that is started waits in
    **STARTING** until the trigger is set, and then reaches
    **STARTED**. Rejected for services of other types.

**untrigger** *service*
:   Clear the trigger. A service already **STARTED** stays started;
    the next start waits for a new **trigger**.

### Shutdown

**shutdown** *kind* [*time*] [**\--fast** | **\--superfast**]
:   Initiate shutdown. *kind* is one of **halt**, **poweroff**,
    **reboot**, **kexec**, **softreboot** / **soft-reboot**. Same
    semantics as the **slinit-shutdown**(8) tool but routed through
    the control socket.

    *time* is **+**\ *N* minutes or *HH:MM* to schedule one, or **now**.
    How much of a hurry slinit is in comes in three steps:

    **shutdown** *kind*
    :   Stop every service properly: **SIGTERM**, then its
        **stop-timeout**, then **SIGKILL** for whatever is left.
        Then sync, unmount and the syscall.

    **shutdown** *kind* **now**
    :   Do not wait: services are **SIGKILL**ed as soon as the
        teardown starts, so a service with a long **stop-timeout**
        cannot hold the machine up. Filesystems are still synced and
        unmounted.

        A **stop-command** that is already running is the one thing
        not killed on the spot: it gets one second first. For a
        service whose daemon is detached — anything built on
        **slinit-start-stop-daemon**(8) or
        **slinit-supervise-daemon**(8) — that script is the only
        thing that will ever stop the daemon, and killing it
        mid-flight leaves the daemon running with its pidfile intact,
        which makes the *next* boot fail to start it. One second is
        far more than such a script needs and far less than the
        **stop-timeout** it stands in for, so the promise above is
        unaffected.

    **shutdown** *kind* **\--fast**
    :   Skip the teardown altogether — sync and the syscall, nothing
        else, which is what **reboot**(8) **-f** does. Services get no
        stop-command and filesystems are not unmounted, so use it when
        the box has to go down now and the state on disk is expendable
        or already safe. Cannot be combined with a scheduled *time*.

        Both this and **\--superfast** below describe work skipped on
        the way to the kernel, so they only differ from **now** when the
        kernel is where the request ends. A **softreboot** re-executes
        slinit in place and is not a **reboot**(2) operation at all, and
        in container mode there is no syscall to make either; in both
        cases the flags behave as **now**, hurrying the teardown, which
        is the only part there is.

    **shutdown** *kind* **\--superfast**
    :   The syscall alone. Everything **\--fast** still did — the
        filesystem sync, the utmp/wtmp record, the saved clock
        timestamp — is skipped as well, which is **reboot**(8) **-ff**.
        Use it when the machine must go down this instant and you
        accept the consequence: anything written but not yet flushed is
        lost, and the next boot finds an unclean filesystem. Like
        **\--fast**, it cannot be scheduled, and in container mode it
        behaves as **now**.

    Further options:

    **-c**, **\--cancel**
    :   Cancel a scheduled shutdown.

    **\--status**
    :   Print the pending scheduled shutdown, if any.

    **-k**, **\--warn**
    :   Broadcast the wall message only; nothing is scheduled. Without
        **-m**, a default maintenance notice is sent.

    **-m**, **\--message** *TEXT*
    :   Wall message to broadcast. Words after *kind* and *time* are
        also joined into the message, as with SysV **shutdown**.

    **-i**, **\--interactive**
    :   Ask for the host name and refuse to proceed unless it matches,
        before anything is sent to the daemon.

**halt** | **poweroff** | **reboot** | **kexec** | **softreboot**
:   Top-level shortcuts equivalent to **shutdown** with the same
    *kind*. Provided so **slinitctl reboot** works as muscle-memory
    from other init systems without having to type the *kind*
    argument.

**suspend** [**\--no-coordination**] [*STATE*]
:   Put the system to sleep. Default *STATE* is **mem**
    (suspend-to-RAM, ACPI S3). Other kernel values: **freeze**
    (suspend-to-idle, s2idle), **standby** (power-on suspend, S1),
    **disk** (hibernate, S4 — successful hibernate does not return).
    The state is validated against the kernel-advertised list in
    */sys/power/state* so an unsupported target produces a clear
    error rather than an opaque EINVAL. Blocks until wake
    (freeze/standby/mem). finit-parity (`initctl suspend`).

    Since 2.5.1 the request goes through **slinit-logind**(8) by
    default, which is what emits *PrepareForSleep* and so what makes
    the screen lock before the machine sleeps. A *block* inhibitor
    refuses the request and the error names its holder.

    **\--no-coordination** writes the kernel state through PID 1
    directly, which is what this command always used to do. The sleep
    hook still runs either side, but no signal is emitted and no
    inhibitor is consulted, so **nothing locks the screen**. Use it to
    test a sleep hook, or on a system with no logind at all.

    *freeze* and *standby* have no *org.freedesktop.login1* method, so
    they always take the direct path; the command says so on stderr
    rather than locking silently failing. The same fallback applies
    when there is no system bus or no daemon on the name — a container
    or an initramfs can still suspend.

**edit** *NAME*
:   Open the on-disk service description for *NAME* in **$VISUAL**
    (falls back to **$EDITOR**, then **vi**), then reload the
    daemon's view once the editor exits with success. Cleaner than
    the `vi /etc/slinit.d/NAME && slinitctl reload NAME` idiom —
    a non-zero editor exit aborts without touching the daemon.
    finit-parity (`initctl edit NAME`) polish.

**switch-root** *NEWROOT* [*INIT*]
:   Transition from an initramfs to the real root filesystem.
    Only meaningful when slinit is running as PID 1 inside an
    initramfs — the operator has mounted the decrypted / LVM /
    NBD / iSCSI real root at *NEWROOT* and now hands off to
    slinit-in-newroot (or any other init at *INIT*, defaulting
    to */sbin/init*). slinit stops every service, kills
    remaining processes, moves */dev*, */proc*, */sys*, */run*
    into *NEWROOT*, deletes the initramfs contents (when */* is
    ramfs / tmpfs), *mount --move NEWROOT /*, **chroot**(2)s
    into it, and **execve**(2)s *INIT* with PID 1 preserved.
    On success the CLI never sees the resulting state — the
    kernel now runs the new *INIT*. finit-parity (`initctl
    switch_root`).

### Misc

**action** *service* *action-name*
:   Run the custom action *action-name* defined on *service* by an
    **extra-command** or **extra-started-command** directive (see
    **slinit-service**(5)). Extra arguments are not passed through.

**list-actions** *service*
:   Print the custom actions defined on *service*.

**is-newer-than** *path1* *path2* / **is-older-than** *path1* *path2*
:   Compare the modification times of two paths, as OpenRC's helpers of
    the same name do. Exit 0 if the relation holds, 1 if it does not or
    a path does not exist, 2 if a path cannot be examined for another
    reason. Needs no running daemon.

**platform**
:   Detect and print the virtualisation or container platform. Needs no
    running daemon.

**completion** [**bash** | **zsh** | **fish**]
:   Print a shell completion script; the default is **bash**. Needs no
    running daemon.

## EXIT STATUS

**0**
:   Command succeeded (or, for predicates, the queried condition was
    true).

**1**
:   Command failed: transport error, daemon-side rejection, unknown
    service, predicate false. The message on stderr says which.

**2**
:   Usage error: an unknown command or global option value, a missing
    or malformed argument (no service name, an unknown signal,
    dependency type or shutdown kind, *KEY* without *=VALUE*, …), or no
    command at all (the usage text is printed). **is-newer-than** and
    **is-older-than** also exit 2 when a path cannot be examined for a
    reason other than not existing.

An unknown command and a bad global option are reported without
contacting the daemon. A command's own arguments are checked once it is
connected, so with no daemon running a malformed but known command fails
with the connection error (1) first.

## EXAMPLES

Bring a service up and print its buffered log:

    slinitctl start nginx
    slinitctl catlog nginx

Stop a service even though it is pinned started:

    slinitctl --force stop database

Reboot the machine:

    slinitctl shutdown reboot

Enable a service to start at boot:

    slinitctl enable nginx                  # daemon running
    slinitctl --offline -d /etc/slinit.d enable nginx   # install time

The offline form needs *boot* to declare its **waits-for.d**
(e.g. `waits-for.d: boot.d`); the link lands in that directory.

Inspect the dependency graph as DOT:

    slinitctl graph | dot -Tsvg > graph.svg

## SEE ALSO

**slinit**(8), **slinit-service**(5), **slinit-monitor**(8),
**slinit-shutdown**(8).
