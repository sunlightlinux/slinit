# NAME

slinit-init-maker - generate a minimal, bootable slinit
service-description directory

# SYNOPSIS

**slinit-init-maker** [*options*]

# DESCRIPTION

**slinit-init-maker** generates a minimal, bootable slinit
service-description directory. It is inspired by **s6-linux-init-maker**
from the skarnet **s6** suite and produces a layout suitable for use
as the argument to **slinit --services-dir** on a real Linux system.

The generator is intentionally opinionated: it emits a handful of
services — **system-init**, optional **system-mounts**,
optional **network**, *N* gettys — wired to a single top-level
**boot** target. Users are expected to grow this directory by dropping
their own service files in alongside the ones produced here.

The default output directory is */etc/slinit/boot.d*. Existing files
are preserved unless **--force** is given.

# OPTIONS

**-d**, **--output** *DIR*
:   Service-description directory to populate. Default:
    */etc/slinit/boot.d*.

**-f**, **--force**
:   Overwrite existing files. Without this, the generator refuses to
    touch any file that already exists in *DIR*.

**-n**, **--dry-run**
:   Print the plan (every file that would be written, including a
    short preview of its contents) without touching the filesystem.
    Combine with **--with-mounts** / **--with-network** etc. to
    inspect the full layout before committing.

**--name** *NAME*
:   Name of the top-level boot target service. Default: **boot**.

**--bin** *PATH*
:   Absolute path to the **slinit** binary, embedded into the
    generated README as a hint for users wiring the kernel cmdline.
    Default: */sbin/slinit*.

**-t**, **--ttys** *N*
:   Number of virtual terminals to generate. Each becomes a service
    named *getty-ttyN* (1-indexed). Default: **6**. Set to 0 to skip
    getty generation entirely.

**--getty** *CMD*
:   Getty binary to exec for each tty. Common choices: */sbin/agetty*
    (util-linux), */sbin/mingetty*. Default: */sbin/agetty*.

**--baud** *N*
:   Baudrate passed to agetty via **--keep-baud**. Ignored for getty
    implementations that don't accept it. Default: **38400**.

**--hostname** *NAME*
:   Initial hostname written to the generated env-file as
    *HOSTNAME=NAME*. Empty skips the entry.

**--tz** *ZONE*
:   Default timezone written to the env-file as *TZ=ZONE*. Empty
    skips the entry.

**--with-mounts**
:   Emit a **system-mounts** service that runs **mount -a**. Enabled
    by default.

**--with-network**
:   Emit a stub **network** service the user can replace with
    something real (NetworkManager, systemd-networkd shim, manual
    iproute2, etc.).

**--with-shutdown-hook**
:   Write a commented sample shutdown hook to
    *DIR*/*shutdown-hook.sample*. The file is not executable by
    default — the operator decides when to wire it in.

**--version**
:   Print the binary version (set at build time via
    **-ldflags '-X main.version=...'**) and exit.

# GENERATED LAYOUT

A typical run with the defaults emits, into *DIR*:

| Path                 | Purpose                                          |
|----------------------|--------------------------------------------------|
| boot                 | Top-level **internal** target; **waits-for**     |
|                      | every other generated service.                   |
| system-init          | One-shot scripted service: hostname, /proc, etc. |
| system-mounts        | One-shot **mount -a** wrapper (if **--with-mounts**). |
| network              | Stub bring-up (if **--with-network**).           |
| getty-ttyN           | One per VT, **runs-on-console=true**.            |
| environment          | env-file consumed by every generated service.    |
| README               | Hints for wiring the kernel cmdline.             |
| shutdown-hook.sample | Commented template (if **--with-shutdown-hook**).|

# EXIT STATUS

**0**
:   Plan executed successfully (or **--dry-run** printed cleanly).

**1**
:   Filesystem error, attempted overwrite without **--force**, or
    other recoverable failure.

**2**
:   Bad command-line arguments.

# EXAMPLES

Generate a default layout under */etc/slinit/boot.d*:

```
slinit-init-maker
```

Generate a 4-tty server layout with a custom output dir and a stubbed
network bring-up:

```
slinit-init-maker -d /etc/slinit/boot.d -t 4 --with-network --hostname node1
```

Inspect what would be written without touching the disk:

```
slinit-init-maker -n -d /tmp/slinit-test --with-shutdown-hook
```

# SEE ALSO

**slinit**(8), **slinitctl**(8), **slinit-service**(5)

# AUTHORS

Ionut Nechita and contributors. slinit is licensed under Apache 2.0.
