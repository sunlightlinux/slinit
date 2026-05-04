# slinitctl 8 "" "" "slinit \- service management system"

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

**-p** *path*, **\--socket-path** *path*
:   Path to the slinit control socket. Defaults to */run/slinit.socket*
    in system mode and *$XDG_RUNTIME_DIR/slinitctl* (or
    *$HOME/.slinitctl*) in user mode.

**-s**, **\--system**
:   Connect to the system service manager.

**-u**, **\--user**
:   Connect to the user service manager (default for non-root users).

**-q**, **\--quiet**
:   Suppress informational output.

**\--no-wait**
:   For commands that normally wait for the target state to be
    reached, return as soon as the request has been accepted.

**\--pin**
:   For **start** and **stop**: pin the service in the requested state
    so that automatic restart / dependency-driven stop cannot move it.
    Use **unpin** to clear.

**-f**, **\--force**
:   For **stop** and **restart**: stop the service even if other
    services still depend on it (forces a cascade stop of dependents).

**\--ignore-unstarted**
:   For **stop** and **restart**: exit 0 silently if the service is
    already stopped, rather than failing.

**-o**, **\--offline**
:   For **enable** / **disable**: work directly on the service files
    without talking to a daemon.

**-d** *dir*, **\--services-dir** *dir*
:   Service directory used by **\--offline** mode.

**\--from** *service*
:   For **enable** / **disable**: name of the *source* service whose
    *waits-for.d/* directory is being modified. Defaults to **boot**.

**\--use-passed-cfd**
:   Take the control-socket file descriptor from the environment
    variable *SLINIT_CS_FD* instead of opening one. Used internally
    when slinit spawns a service that wants to make control calls.

**-h**, **\--help**
:   Show usage and exit.

**\--version**
:   Show version and exit.

## COMMANDS

### Service lifecycle

**start** *service*
:   Activate *service*. Starts dependencies as needed.

**wake** *service*
:   Like **start**, but only if the service is currently stopped
    because none of its hard-dependents are active. Used to "rejoin"
    a previously released service.

**stop** *service*
:   Stop *service*. Fails (without effect) if other services still
    depend on it, unless **\--force** is given.

**release** *service*
:   Remove explicit activation from *service*. Stops it iff no other
    active service still requires it.

**restart** *service*
:   Stop and then start *service*.

**signal** [**-l** | **\--list**] *signal* *service*
:   Send *signal* to the service's main process. *signal* may be a
    name (`HUP`, `TERM`, `USR1`, …) or a number. **-l** lists the
    accepted signal names.

**pause** *service*
:   Send SIGSTOP to the service's process group. The service remains
    in the *running* state from slinit's point of view.

**continue** *service* (alias **cont**)
:   Counterpart to **pause**: send SIGCONT.

**once** *service*
:   Like **start**, but disable any *restart=on-failure* policy for
    this run — a one-shot-style execution.

**unpin** *service*
:   Clear a previous **\--pin** on *service*.

### Status & queries

**list** (alias **ls**)
:   List all loaded services and their state (started / stopped /
    starting / stopping / failed).

**status** *service*
:   Print a multi-line status block for *service*.

**is-started** *service*
:   Exit 0 iff *service* is currently *started*; non-zero otherwise.
    Suitable for shell scripting.

**is-failed** *service*
:   Exit 0 iff *service* failed at its last attempt.

**dependents** *service*
:   Print services that hard-depend on *service*.

**query-name**
:   Print the daemon's idea of its own service-name (set via
    *SLINIT_SERVICENAME* in slinit's own environment, used by
    consumer-of). Useful from inside a service.

**service-dirs**
:   Print the list of service directories the daemon is searching.

**query-load-mech** (alias **load-mech**)
:   Print the daemon's load mechanism (which is currently always
    *file*; reserved for future load backends).

**boot-time** (alias **analyze**)
:   Print boot-time analysis: kernel→userspace handoff, slinit
    startup, per-service start times, slow services.

**catlog** [**\--clear**] *service*
:   Print *service*'s in-memory log buffer. **\--clear** truncates the
    buffer after printing.

**graph** [*service*]
:   Print the dependency graph as Graphviz DOT. With no argument the
    full graph is printed; with a service name only that subgraph.

**list5**, **status5** *service*
:   Same output as **list** / **status** but using the v5 wire
    protocol, which adds *stop_reason*, *exec_stage* and *si_code* /
    *si_status* fields. Useful for debugging service exits.

**attach** *service*
:   Stream the service's log output (catlog plus a tail-follow on the
    pipe). Press *^C* to detach.

### Configuration & environment

**reload** *service*
:   Re-read *service*'s description from disk. Some changes apply
    immediately; others require the service to restart. The daemon
    rejects reloads that would change the service type or invalidate
    in-flight state.

**reload-all**
:   Re-read every loaded service description from disk in one round
    trip. Services in transitional states (**STARTING** / **STOPPING**)
    are skipped silently — operators retry once the service settles.
    Prints a summary like "Reloaded 12 service(s)" on success or
    "Reloaded 11 service(s); 1 failed" with a non-zero exit when one
    or more reloads were rejected. The per-service rules of **reload**
    apply (no type change, must be in a stable state). Typical use:
    ops applied a config update across many service files and want
    them all picked up without scripting a `for` loop.

**unload** *service*
:   Drop *service* from the in-memory set. Only allowed when the
    service is stopped and not a dependency of an active service.

**add-dep** *kind* *from* *to*
:   Add a dependency edge of *kind* (`depends-on`/`regular`,
    `waits-for`/`soft`, `depends-ms`/`milestone`, `before`, `after`)
    from *from* to *to*.

**rm-dep** *kind* *from* *to*
:   Remove a dependency edge of *kind*.

**enable** *service* [\--from *src*]
:   Enable *service* by creating a symlink in *src*'s *waits-for.d/*
    directory (default *src* is **boot**). Without a daemon, with
    **\--offline**.

**disable** *service* [\--from *src*]
:   Inverse of **enable**.

**setenv** *KEY*[=*VALUE*]
:   Set an environment variable for newly-started services. Without
    *=VALUE*, copies from slinitctl's own environment.

**unsetenv** *KEY*
:   Remove a previously-set environment variable.

**getallenv**
:   Print the daemon's full environment (one *KEY*=*VALUE* per line).

**setenv-global** / **unsetenv-global** / **getallenv-global**
:   Same as the **setenv** family but operate on the global
    environment (handle 0): the values are inherited by every service
    rather than installed on a single one.

**trigger** *service*
:   Mark a *type=triggered* service as triggered (it will start
    once its dependencies are satisfied).

**untrigger** *service*
:   Reset the triggered flag.

### Shutdown

**shutdown** *kind*
:   Initiate shutdown. *kind* is one of **halt**, **poweroff**,
    **reboot**, **kexec**, **softreboot** / **soft-reboot**. Same
    semantics as the **slinit-shutdown**(8) tool but routed through
    the control socket.

### Misc

**action** *service* *action-name* [*args...*]
:   Invoke a custom *action* defined on *service* via its
    *action.d/* directory or *control-command-N=* settings.

**list-actions** *service*
:   Print available actions for *service*.

**is-newer-than** *path1* *path2* / **is-older-than** *path1* *path2*
:   Compare mtime of two paths. Exit 0 if the relation holds.

**bash** | **zsh** | **fish**
:   Print a shell completion script.

## EXIT STATUS

**0**
:   Command succeeded (or, for predicates, the queried condition was
    true).

**1**
:   Command failed (transport error, daemon-side rejection, predicate
    false, …). The error message on stderr distinguishes.

**2**
:   Usage error (bad option, missing argument).

## EXAMPLES

Bring a service up and tail its log:

    slinitctl start nginx
    slinitctl attach nginx

Forcefully stop a service that has dependents:

    slinitctl stop --force database

Reboot the machine:

    slinitctl shutdown reboot

Enable a service to start at boot:

    slinitctl enable nginx                  # daemon running
    slinitctl --offline -d /etc/slinit.d enable nginx   # initramfs / install time

Inspect the dependency graph as DOT:

    slinitctl graph | dot -Tsvg > graph.svg

## SEE ALSO

**slinit**(8), **slinit-service**(5), **slinit-monitor**(8),
**slinit-shutdown**(8).
