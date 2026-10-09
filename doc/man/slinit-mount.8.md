% SLINIT-MOUNT(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-07-21

# NAME

slinit-mount - autofs lazy mount daemon for slinit

# SYNOPSIS

**slinit-mount** [*options*]

**slinit-mount** **-d** */etc/slinit.d/mount.d* **--foreground**

# DESCRIPTION

**slinit-mount** is a small daemon that sets up autofs mount points so
that filesystems are mounted on demand the first time something
accesses them, and (optionally) unmounted again after an idle timeout.

It is meant to be supervised as an ordinary slinit service and acts as
a slinit-native replacement for systemd's automount unit type. Mount
points are described by **\*.mount** files directly inside one or
more "mount-unit" directories (a directory that does not exist is
skipped); **slinit-mount** scans those directories,
sets up an autofs trigger for each entry, and reconciles the live state
on **SIGHUP**.

For one-shot mounting at boot, use a regular scripted service that
runs **mount**(8) directly — **slinit-mount** is for the lazy-mount
case where the operator prefers paying the mount cost on first access.

# OPTIONS

**-d**, **--mount-dir** *DIR*
:   Mount-unit directory to scan. May be repeated. Default:
    */etc/slinit.d/mount.d*.

**-f**, **--foreground**
:   Accepted for compatibility. **slinit-mount** never daemonises; it
    always runs in the foreground, as a slinit-supervised process
    should.

**-v**, **--verbose**
:   Verbose logging — every mount/unmount and every reconcile pass
    is logged.

**-p**, **--socket-path** *PATH*
:   slinit control socket used to wait for **after** services.
    Default: the socket of the instance **slinit-mount** runs under,
    resolved as **slinitctl**(8) does — *$DINIT_SOCKET_PATH* or
    *$SLINIT_SOCKET_PATH* if set, else */run/slinit.socket* when run
    as root, otherwise *$XDG_RUNTIME_DIR/slinitctl*, or
    *$HOME/.slinitctl* when **XDG_RUNTIME_DIR** is unset. Only
    contacted when some unit uses **after**.

**--expire-interval** *N*
:   Seconds between idle-timeout sweeps. Default: **60**. Mount units
    with **timeout=0** (or no **timeout**) are never expired regardless
    of this value.

**-h**, **--help**
:   Print a usage summary and exit.

# MOUNT UNIT FORMAT

A mount-unit file is a key=value document; the unit's name is the file
name without **.mount**. Blank lines and lines starting with **#** are
ignored, and an unrecognised key is an error. Recognised keys:

**description**
:   Free-form description.

**what**
:   Source device or path (e.g. */dev/sda1*; required).

**where**
:   Mount point (absolute path; required).

**type**
:   Filesystem type (required).

**options**
:   Mount options (passed verbatim to **mount**(2)).

**timeout**
:   Idle timeout in whole seconds. **0**, the default, means "never
    auto-unmount".

**autofs-type**
:   **indirect** (default) or **direct**, matching the autofs flavours.

**directory-mode**
:   Permissions, in octal, used for auto-created mount-point
    directories. Default **0755**.

**after**
:   slinit services that must be STARTED before the autofs mount is
    set up (**after: a b** for several names). The unit is set up
    only once all of them are started, however long that takes: there
    is no timeout. **slinit-mount** logs once that the unit is waiting
    and on which services, and follows their state over the control
    socket (see **--socket-path**). If the socket cannot be reached,
    or a named service does not exist, that is logged and retried
    every few seconds. A service that stops after the mount is set up
    does not tear it down: the mount stays until the unit is removed
    or changed on reload, or the daemon exits. Units without **after**
    are set up immediately.

# RELOAD

On **SIGHUP**, **slinit-mount** re-reads every mount-unit directory,
diffs the result against the running state (units are matched by
**where**), and:

- tears down units that have been removed or whose configuration
  changed in a way that requires re-establishing the autofs mount,
- registers any new units,
- leaves unchanged units alone, whether already set up or still
  waiting for their **after** services.

A change to **after** counts as a configuration change like any other
key: the unit is torn down (or stops waiting) and is then deferred
again until its new **after** services are all started.

This is the recommended way to deploy a new mount unit without
disturbing in-flight access to the others. If any file fails to parse
or validate, the whole reload is abandoned and the running state is
kept.

# EXIT STATUS

**0**
:   Clean shutdown (received **SIGTERM** or **SIGINT**), or no mount
    units were found at start-up — the daemon logs that and exits.

**1**
:   Fatal error: a mount-unit file failed to parse or validate at
    start-up (one bad file stops all of them), no autofs mount could be
    established and no unit is waiting for its **after** services, an
    unknown option, or a runtime failure.

# EXAMPLES

Run as a slinit service in the foreground:

```
slinit-mount --foreground -d /etc/slinit.d/mount.d
```

Trigger a config reload after dropping a new **.mount** file:

```
slinitctl signal HUP slinit-mount
```

# SEE ALSO

**slinit**(8), **slinitctl**(8), **slinit-service**(5),
**mount**(8), **autofs**(5)

# AUTHORS

Ionut Nechita and contributors. slinit is licensed under Apache 2.0.
