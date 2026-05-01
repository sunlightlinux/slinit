# NAME

slinit-shutdown - issue a shutdown, halt, reboot, or soft-reboot to slinit

# SYNOPSIS

**slinit-shutdown** [**-r** | **-h** | **-p** | **-s** | **-k**]
[**--system**] [**--use-passed-cfd**] [**--grace=***DURATION*]

**slinit-reboot** [*options*]

**slinit-halt** [*options*]

**slinit-poweroff** [*options*]

**slinit-soft-reboot** [*options*]

# DESCRIPTION

**slinit-shutdown** is the standalone shutdown utility for **slinit**(8).
By default it connects to the running daemon over the control socket
and asks it to perform an orderly shutdown — services are stopped in
reverse dependency order, the **shutdown** target is brought up,
filesystems are flushed, and the kernel is reset.

The action is selected by either an option flag or by the name the
binary was invoked under (so the conventional **halt**, **reboot**,
**poweroff**, **soft-reboot** wrappers all map onto the same code path).

Most operators should prefer **slinitctl shutdown** — it does the same
thing through the same protocol. **slinit-shutdown** exists for the
narrower class of contexts where slinitctl is unavailable: minimal
recovery shells, install media, container images shipping only a
pinned shutdown helper, and the daemon's own internal **shutdown**
target.

# ACTIONS

**-r**
:   Reboot. Default when invoked as **slinit-reboot**.

**-h**
:   Halt the system (CPU stopped, no power-off). Default when invoked
    as **slinit-halt**.

**-p**
:   Power off. Default action when invoked as **slinit-shutdown** or
    **slinit-poweroff**.

**-s**
:   Soft-reboot — re-exec **slinit** in place with the same arguments,
    keeping the kernel running. Default when invoked as
    **slinit-soft-reboot**. Combined with **--system**, this performs
    only the userspace teardown (kill-all + sync) and then exits, so
    the parent slinit can re-exec itself.

**-k**
:   Kexec into a previously loaded kernel (via **kexec_load**(2) /
    **kexec --load**). The action sequence matches **-r** until the
    final reset call, which boots the new kernel instead.

# OPTIONS

**--system**
:   Skip the daemon and perform the shutdown sequence directly:
    kill every process, run the configured kill-grace timer, sync
    filesystems, then issue the kernel reset for the chosen action.
    Reserved for slinit's own internal use during the **shutdown**
    target — invoking it manually on a running system bypasses
    every dependency and timeout the daemon would otherwise honour
    and will cause data loss.

**--use-passed-cfd**
:   Use the file descriptor exported in **$SLINIT_CS_FD** as the
    control-socket connection instead of dialing
    */run/slinit.socket*. Used when slinit hands off the socket
    directly to the shutdown helper to avoid relying on filesystem
    state during late shutdown.

**--grace=***DURATION*
:   Override the SIGTERM-to-SIGKILL grace period for **--system** mode.
    Accepts any duration parseable by **time.ParseDuration** (e.g.
    *5s*, *250ms*, *2m*).

**--help**
:   Print a usage summary and exit.

# DEFAULT ACTION

If invoked as **slinit-shutdown** with no action flag, the default is
**-p** (power off). Symlinks override this default based on basename:

| Invocation name        | Default action     |
|------------------------|--------------------|
| slinit-reboot          | reboot (-r)        |
| slinit-halt            | halt (-h)          |
| slinit-poweroff        | poweroff (-p)      |
| slinit-soft-reboot     | soft-reboot (-s)   |
| slinit-shutdown        | poweroff (-p)      |

# ENVIRONMENT

**SLINIT_CS_FD**
:   When **--use-passed-cfd** is set, the integer file descriptor of
    a pre-opened control-socket connection.

# EXIT STATUS

**0**
:   Reserved — **slinit-shutdown** normally does not return: the daemon
    runs the **shutdown** target around it, and either the kernel is
    reset or the process is killed.

**1**
:   Failed to connect to the daemon, malformed argument, protocol
    version mismatch, or a fatal failure during **--system** mode.

# SEE ALSO

**slinit**(8), **slinitctl**(8), **slinit-service**(5),
**reboot**(2), **kexec_load**(2)

# AUTHORS

Ionut Nechita and contributors. slinit is licensed under Apache 2.0.
