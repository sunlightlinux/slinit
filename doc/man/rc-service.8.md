# NAME

rc-service - OpenRC-compatible service control shim for slinit

# SYNOPSIS

**rc-service** *service* *action*

**rc-service** **-e** | **--exists** *service*

**rc-service** **-l** | **--list**

**rc-service** **-r** | **--resolve** *service*

# DESCRIPTION

**rc-service** is a thin compatibility wrapper that accepts the
OpenRC-style argv shape and translates it into an equivalent
**slinitctl**(8) invocation. It exists so that automation, packages,
and operator muscle memory built around **rc-service nginx restart**
keep working unchanged on a slinit-managed system.

The translation is performed in-process; once an argv has been
mapped, **rc-service** **exec(3)**s **slinitctl** so the exit status
flows directly back to the caller. There is no daemon round-trip
beyond what **slinitctl** itself performs.

# ACTIONS

The following OpenRC verbs are recognised and translated:

**start**, **stop**, **restart**, **status**, **pause**, **continue**
:   Mapped 1:1 to the same-named **slinitctl** subcommands.

**zap**
:   OpenRC's "force back to stopped regardless of current state".
    Translated to **slinitctl release** *service*, which clears any
    pinning so the service is free to be stopped by ordinary means.

Any other action is passed through verbatim, so future OpenRC verbs
that happen to have a slinitctl equivalent keep working.

# OPTIONS

**-e**, **--exists** *service*
:   Translates to **slinitctl is-started** *service*. Exit status 0
    means the service exists and is started; nonzero means it is
    not currently running (or does not exist on disk).

**-l**, **--list**
:   Translates to **slinitctl list**.

**-r**, **--resolve** *service*
:   Translates to **slinitctl query-name** *service*. OpenRC's
    **--resolve** prints an absolute init-script path; slinit has
    no equivalent of an init-script path, so **query-name** — which
    reports the canonical name slinit knows the service under —
    is the closest match.

**-h**, **--help**
:   Print a usage summary and exit.

# ENVIRONMENT

**SLINITCTL**
:   Path to the **slinitctl** binary. Defaults to **slinitctl** (which
    is then resolved against **PATH**). Absolute paths are passed
    through as-is. Distributions that install slinit under a non-default
    prefix can set this in the global profile so all OpenRC shims keep
    working.

# EXIT STATUS

The exit status of the underlying **slinitctl** invocation is returned
verbatim. Additionally:

**127**
:   **SLINITCTL** could not be located on **PATH**.

**2**
:   Usage error (bad argv shape).

# EXAMPLES

```
rc-service nginx restart
rc-service postgres status
rc-service redis zap
rc-service --exists prometheus
rc-service --list
```

# SEE ALSO

**slinit**(8), **slinitctl**(8), **rc-update**(8), **rc-status**(8),
**slinit-service**(5)

# AUTHORS

Ionut Nechita and contributors. slinit is licensed under Apache 2.0.
