# NAME

rc-status - OpenRC-compatible service status listing for slinit

# SYNOPSIS

**rc-status**

**rc-status** *runlevel*

**rc-status** **-l** | **--list**

**rc-status** **-r** | **--runlevel**

**rc-status** **-a** | **--all**

**rc-status** **-s** | **--servicelist**

**rc-status** **-u** | **--unused**

# DESCRIPTION

**rc-status** is a thin projection of **slinitctl list** under the
OpenRC argv shape. It prints the current state of every loaded service
or, when given a runlevel name, the dependency graph rooted at that
runlevel.

OpenRC's native **rc-status** ships a per-runlevel grouped layout with
coloured **OK** / **STOPPED** markers. **rc-status** under slinit does
not reproduce that exact formatting — **slinitctl list** already
distinguishes states with its own conventions, and forcing OpenRC's
colour codes through would be a maintenance burden. Operators who want
the exact OpenRC look can script it on top of **slinitctl list5**.

# ACTIONS

(no arguments)
:   Print every loaded service. Translates to **slinitctl list**.

*runlevel*
:   A bare positional argument is treated as a runlevel name. Translates
    to **slinitctl graph** *runlevel-NAME* — the dependency graph
    rooted at that runlevel service.

**-a**, **--all**
:   Same as no arguments.

**-s**, **--servicelist**
:   Same as no arguments.

**-l**, **--list**
:   Print the canonical OpenRC runlevel names: **sysinit**, **boot**,
    **default**, **nonetwork**, **shutdown**. Custom runlevels defined
    by the admin are not enumerated here — discovering them would
    require a full **slinitctl list** round-trip, which the **-a**
    form already does.

**-r**, **--runlevel**
:   Print the current runlevel. slinit has no concept of a "current"
    runlevel, so this always prints **default** as a stable stand-in.
    This matches the steady-state value a fully booted OpenRC system
    would report and keeps scripts that depend on the value working.

**-u**, **--unused**
:   OpenRC lists services not in any runlevel. slinit has no direct
    equivalent (it would require walking every dep graph), so this
    falls back to the full **slinitctl list** output.

**-h**, **--help**
:   Print a usage summary and exit.

# ENVIRONMENT

**SLINITCTL**
:   Path to the **slinitctl** binary. Defaults to **slinitctl** (resolved
    against **PATH**).

# EXIT STATUS

The exit status of the underlying **slinitctl** invocation is returned
verbatim. Additionally:

**0**
:   Returned for **--list** and **--runlevel** without dispatching to
    **slinitctl**.

**127**
:   **SLINITCTL** could not be located on **PATH**.

**2**
:   Unknown flag.

# EXAMPLES

```
rc-status                # all services
rc-status default        # graph rooted at runlevel-default
rc-status --list         # canonical runlevel names
rc-status --runlevel     # always "default"
```

# SEE ALSO

**slinit**(8), **slinitctl**(8), **rc-service**(8), **rc-update**(8),
**slinit-service**(5)

# AUTHORS

Ionut Nechita and contributors. slinit is licensed under Apache 2.0.
