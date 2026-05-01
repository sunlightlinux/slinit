# NAME

slinit-nuke - emergency kill-all utility for slinit-managed systems

# SYNOPSIS

**slinit-nuke** [**--grace** *DURATION*] [**-9** | **--kill-only**]

# DESCRIPTION

**slinit-nuke** sends **SIGTERM** to every process reachable via
**kill(-1, sig)**, waits a short grace period, then follows up with
**SIGKILL**. It is the deliberate-sledgehammer recovery utility for
the rare scenario where the normal shutdown path is unavailable —
typically because **slinit**(8) itself has hung — and an operator
needs to clear userspace before re-execing or manually rebooting.

It does **not** unmount filesystems, sync the page cache, or
coordinate with slinit. For orderly shutdowns use
**slinitctl shutdown** or the **slinit-shutdown**(8) family.

# OPTIONS

**--grace** *DURATION*
:   Time between **SIGTERM** and **SIGKILL**. Accepts any duration
    parseable by **time.ParseDuration** (e.g. *2s*, *250ms*, *1m*).
    A zero or negative value sends **SIGKILL** immediately.
    Default: **2s**.

**-9**, **--kill-only**
:   Skip the **SIGTERM** phase and send **SIGKILL** directly. Useful
    when the operator knows everything is unresponsive and the grace
    period is just lost time.

**-h**, **--help**
:   Print a usage summary and exit.

# BEHAVIOUR

The signals are best-effort. **ESRCH** ("no processes matched") is
silently ignored — it is a valid outcome (the only userspace process
left could be **slinit-nuke** itself, and that is fine).

Other errors (e.g. **EPERM**) are reported on stderr and the
corresponding phase is treated as failed. The final **SIGKILL** failure
is the only one that produces a nonzero exit.

# EXIT STATUS

**0**
:   The **SIGKILL** broadcast succeeded (or returned **ESRCH**, which
    is also success).

**1**
:   The final **SIGKILL** broadcast failed for a non-**ESRCH** reason.

**2**
:   Bad command-line arguments.

# EXAMPLES

Standard "graceful then forceful" wipe with the default 2-second
grace:

```
slinit-nuke
```

Immediate **SIGKILL** broadcast:

```
slinit-nuke -9
```

Generous 5-second grace (e.g. for hosts running large databases):

```
slinit-nuke --grace 5s
```

# SEE ALSO

**slinit**(8), **slinitctl**(8), **slinit-shutdown**(8),
**kill**(2)

# AUTHORS

Ionut Nechita and contributors. slinit is licensed under Apache 2.0.
