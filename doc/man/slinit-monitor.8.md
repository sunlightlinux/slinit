# NAME

slinit-monitor - watch slinit service and environment events, run a
command on each change

# SYNOPSIS

**slinit-monitor** [*options*] **-c** *COMMAND* *service-name*...

**slinit-monitor** [*options*] **-E -c** *COMMAND* [*var-name*...]

# DESCRIPTION

**slinit-monitor** connects to a running **slinit**(8) instance, subscribes
to push notifications for the named services or for the global environment,
and executes *COMMAND* every time a relevant event arrives.

It is the slinit equivalent of **s6-svstat -E** / **s6-rc-svc-listen**:
a small bridge between the daemon's event stream and ordinary shell
tooling. Typical uses are:

- Triggering a notification (mail, log entry, push) when a service
  fails or restarts.
- Reloading a downstream consumer when a value in slinit's environment
  table changes.
- Driving a watchdog that shells out to recovery logic on a specific
  failure event.

By default, **slinit-monitor** runs forever, firing *COMMAND* once per
event. Use **--exit** to make it exit after the first command run.

# MODES

## Service mode (default)

Each positional argument is a service name. **slinit-monitor** loads
each service to obtain a control-protocol handle, then subscribes to
its event stream. Five event types are reported:

- **started** — the service reached the started state.
- **stopped** — the service reached the stopped state.
- **failed** — the service failed to start.
- **start-cancelled** — a pending start was cancelled.
- **stop-cancelled** — a pending stop was cancelled.

## Environment mode (**-E**)

Subscribes to the global environment-change feed. Each event delivers
either *KEY=VALUE* (a set, mapped to status text **set**) or just *KEY*
(an unset, mapped to **unset**).

If positional *var-name* arguments are supplied, only changes to those
variables fire *COMMAND*. Otherwise every change fires it.

# COMMAND SUBSTITUTIONS

Before *COMMAND* is executed, the following placeholders are replaced:

**%n**
:   Service name (service mode) or variable name (env mode).

**%s**
:   Status text. Defaults are **started**, **stopped**, **failed**,
    **set**, **unset**; override via the **--str-...** options below.

**%v**
:   Variable value (env mode only; empty for unsets and in service
    mode).

**%%**
:   A literal **%** sign.

The result is split on unquoted whitespace; double-quoted segments are
preserved as a single argument. **slinit-monitor** does **not** spawn
a shell — quote your command accordingly, or wrap it in
**sh -c "..."**.

# OPTIONS

**-c**, **--command** *COMMAND*
:   Command to execute on each event. Required.

**-E**, **--env**
:   Switch to environment-monitor mode (see above).

**-i**, **--initial**
:   Fire *COMMAND* once for the **current** state at startup, before
    waiting for events. In service mode this delivers each service's
    state right after the load. In env mode it walks the global
    environment table.

**-e**, **--exit**
:   Exit after the first command run. Combined with **--initial**, this
    yields a one-shot "fetch current state and report".

**-s**, **--system**
:   Use the system socket (*/run/slinit.socket*).

**-u**, **--user**
:   Use the per-user socket (default *~/.slinitctl*).

**-p**, **--socket-path** *PATH*
:   Override the control socket path explicitly.

**--str-started** *TEXT*
:   Replace the default text emitted as **%s** for **started** events.

**--str-stopped** *TEXT*
:   Same, for **stopped** and **stop-cancelled** events.

**--str-failed** *TEXT*
:   Same, for **failed** events.

**--str-set** *TEXT*
:   Same, for env **set** events.

**--str-unset** *TEXT*
:   Same, for env **unset** events.

**-h**, **--help**
:   Print a usage summary and exit.

# EXAMPLES

Send a desktop notification whenever **nginx** changes state:

```
slinit-monitor -c 'notify-send "nginx %s"' nginx
```

Reload a downstream consumer when DATABASE_URL is set or unset:

```
slinit-monitor -E -c '/usr/local/bin/reconfigure %n %s' DATABASE_URL
```

Wait for **postgres** to become **started**, then exit:

```
slinit-monitor --initial --exit -c 'true' postgres
```

Run a recovery script the first time **worker** fails:

```
slinit-monitor --exit \
    --str-failed dead \
    -c '/usr/local/bin/recover %n %s' worker
```

# EXIT STATUS

**0**
:   Normal exit (only reachable with **--exit**, when an event has
    been observed and the command finished).

**1**
:   Connection error, version-handshake failure, or a fatal usage
    problem (no services in service mode, no command, etc.).

# SEE ALSO

**slinit**(8), **slinitctl**(8), **slinit-check**(8),
**slinit-service**(5)

# AUTHORS

Ionut Nechita and contributors. slinit is licensed under Apache 2.0.
