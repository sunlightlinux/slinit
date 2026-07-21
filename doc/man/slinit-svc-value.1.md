% SLINIT-SVC-VALUE(1) slinit | Sunlight Linux
% Ionut Nechita
% 2026-07-21

# NAME

slinit-svc-value, service_get_value, service_set_value,
service_export, get_options, save_options -
per-service persistent key=value store (OpenRC-compatible)

# SYNOPSIS

*APPLET* *ARG*...

where *APPLET* is a symlink named after any of the supported OpenRC
applets (see **APPLETS** below), pointing at the **slinit-svc-value**
binary.

# DESCRIPTION

**slinit-svc-value** is a drop-in replacement for OpenRC's **value**
family: a per-service key=value store an init.d script can use to
persist state across separate **start()** and **stop()** invocations.
The canonical use is remembering runtime state — which cgroup
controllers were mounted, which pidfile the daemon wrote, which
config knob was active — so **stop()** can undo it precisely.

One binary dispatches every applet by inspecting **basename**
(**argv[0]**); installers ship a symlink per applet.

Backing: one file per key at

    $RC_SVCDIR/options/$SVCNAME/$KEY

byte-for-byte compatible with OpenRC's librc layout. The **$RC_SVCDIR**
default is **/run/slinit**.

# APPLETS

**service_get_value** *KEY*
:   Read the value stored under *KEY* and print it to stdout with no
    trailing newline (matches OpenRC). Exit **0** on hit, **1** on
    miss.

**get_options** *KEY*
:   Legacy alias for **service_get_value**.

**service_set_value** *KEY* [*VALUE*]
:   Persist *VALUE* under *KEY*. If *VALUE* is omitted or empty, the
    key is deleted.

**save_options** *KEY* [*VALUE*]
:   Legacy alias for **service_set_value**.

**service_export** *VAR*...
:   For each named environment variable, capture its current value
    into the store — but only if the key is not already present. A
    variable that is unset in the environment is reported to stderr
    and skipped. Idempotent: repeated calls with the same list are
    safe.

# ENVIRONMENT

**RC_SVCNAME** (or **SLINIT_SERVICENAME**) — service the values
belong to. Required; the applet exits with code **2** when neither
is set.

**RC_SVCDIR** — alternative runtime dir. Defaults to
**/run/slinit**.

# EXIT STATUS

- **0**: success.
- **1**: **service_get_value** could not find the key, or a write
  failed.
- **2**: bad usage — missing arguments, missing service name, or an
  invalid key (empty, containing **/** or **\0**, or **.** / **..**).

# EXAMPLES

An init.d script that stores its pidfile path on start and reads it
back on stop:

```
start() {
    ... spawn daemon ...
    service_set_value pidfile "$PIDFILE"
}

stop() {
    pidfile=$(service_get_value pidfile) || pidfile=/run/default.pid
    kill "$(cat "$pidfile")"
    service_set_value pidfile   # empty value → delete
}
```

Snapshot a set of environment variables into the store once at first
boot:

```
service_export DAEMON_UID DAEMON_GID LISTEN_PORT
```

# SEE ALSO

**value**(1) (OpenRC), **service**(1) (OpenRC),
**slinit-einfo**(8), **slinit**(8).
