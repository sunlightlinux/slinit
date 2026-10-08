% SLINIT-MONITOR(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-07-21

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
event. Use **--exit** to make it exit after the first event.

# MODES

## Service mode (default)

Each positional argument is a service name. **slinit-monitor** loads
each service to obtain a control-protocol handle, then subscribes to
its event stream. Every event runs *COMMAND*; **%s** is set as
follows:

| Event | **%s** |
|---|---|
| the service reached the started state | **started** |
| the service reached the stopped state | **stopped** |
| the service failed to start | **failed** |
| a pending start was cancelled | **stopped** |
| a pending stop was cancelled | **started** |
| memory, CPU or IO pressure (PSI) crossed the threshold set by **memory-pressure-\***, **cpu-pressure-\*** or **io-pressure-\*** | **unknown(5)**, **unknown(6)** or **unknown(7)** respectively |

The pressure events fire whenever the *some* avg-window value exceeds
the threshold within a 2-second polling window; there is no repeat
suppression, so a service that stays under pressure fires every window
until it drops back.

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
    See **MODES** for which text each event produces.

**%v**
:   Variable value (env mode only; empty for unsets and in service
    mode).

**%%**
:   A literal **%** sign.

The result is split on unquoted whitespace; double-quoted segments are
preserved as a single argument (the quotes themselves are removed, and
there is no escape character, so a double quote cannot appear inside
an argument). Single quotes have no special meaning. **slinit-monitor**
does **not** spawn a shell: shell syntax such as **&&**, **|** or
**[ ... ]** is passed to the command as plain arguments. Put any logic
in a script and call that, passing **%n** and **%s** as arguments.

A command that fails is reported on stderr; monitoring continues.

# OPTIONS

**-c**, **--command** *COMMAND*
:   Command to execute on each event. Required.

**-E**, **--env**
:   Switch to environment-monitor mode (see above).

**-i**, **--initial**
:   Fire *COMMAND* once for the **current** state at startup, before
    waiting for events. In service mode this delivers each service's
    state right after the load: **%s** is the started text when the
    service is started and the stopped text in every other state. In
    env mode it walks the global environment table, reporting each
    variable as **set**.

**-e**, **--exit**
:   Exit after the first event has run *COMMAND*. The commands run by
    **--initial** do not count, so **--initial --exit** reports the
    current state and then waits for one change.

**-s**, **--system**
:   Use the system socket (*/run/slinit.socket*).

**-u**, **--user**
:   Use the per-user socket, *~/.slinitctl*. Without **-s**, **-u** or
    **-p**, root uses the system socket and other users the per-user
    one.

**-p**, **--socket-path** *PATH*
:   Override the control socket path explicitly.

**--str-started** *TEXT*
:   Replace the default text emitted as **%s** for **started** and
    stop-cancelled events.

**--str-stopped** *TEXT*
:   Same, for **stopped** and start-cancelled events.

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

Report **postgres**'s current state, then exit at its next change:

```
slinit-monitor --initial --exit -c 'echo postgres is %s' postgres
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
    been observed and the command finished). A failing *COMMAND* does
    not change the exit status.

**1**
:   Connection error (including the daemon closing the connection),
    version-handshake failure, a service that cannot be loaded, or a
    usage problem (no services in service mode, no command, unknown
    option, etc.).

# SEE ALSO

**slinit**(8), **slinitctl**(8), **slinit-check**(8),
**slinit-service**(5)

# AUTHORS

Ionut Nechita and contributors. slinit is licensed under Apache 2.0.
