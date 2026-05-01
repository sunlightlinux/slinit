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
points are described by **.mount** files in one or more
"mount-unit" directories; **slinit-mount** scans those directories,
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
:   Run in the foreground (don't daemonise). Required for use as a
    slinit-supervised process.

**-v**, **--verbose**
:   Verbose logging — every mount/unmount and every reconcile pass
    is logged.

**--expire-interval** *N*
:   Seconds between idle-timeout sweeps. Default: **60**. Mount units
    with **timeout=0** are never expired regardless of this value.

**-h**, **--help**
:   Print a usage summary and exit.

# MOUNT UNIT FORMAT

A mount-unit file is a key=value document. Recognised keys:

**what**
:   Source device or path (e.g. */dev/sda1*).

**where**
:   Mount point (absolute path; required).

**type**
:   Filesystem type (required).

**options**
:   Mount options (passed verbatim to **mount**(2)).

**timeout**
:   Idle timeout in seconds. **0** means "never auto-unmount".

**autofs-type**
:   **indirect** (default) or **direct**, matching the autofs flavours.

**directory-mode**
:   Permissions used for auto-created mount-point directories
    (e.g. **0755**).

**after**
:   slinit service-dependency expression — for example
    **after: network-online** to defer mount setup until that
    service has started.

# RELOAD

On **SIGHUP**, **slinit-mount** re-reads every mount-unit directory,
diffs the result against the running state, and:

- tears down units that have been removed or whose configuration
  changed in a way that requires re-establishing the autofs mount,
- registers any new units,
- leaves unchanged units alone.

This is the recommended way to deploy a new mount unit without
disturbing in-flight access to the others.

# EXIT STATUS

**0**
:   Clean shutdown (received **SIGTERM** or **SIGINT**).

**1**
:   Fatal startup or runtime error.

# EXAMPLES

Run as a slinit service in the foreground:

```
slinit-mount --foreground -d /etc/slinit.d/mount.d
```

Trigger a config reload after dropping a new **.mount** file:

```
slinitctl signal --signal HUP slinit-mount
```

# SEE ALSO

**slinit**(8), **slinitctl**(8), **slinit-service**(5),
**mount**(8), **autofs**(5)

# AUTHORS

Ionut Nechita and contributors. slinit is licensed under Apache 2.0.
