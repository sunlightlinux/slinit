# NAME

rc-update - OpenRC-compatible runlevel membership tool for slinit

# SYNOPSIS

**rc-update** **add** *service* [*runlevel*]

**rc-update** **del** | **delete** *service* [*runlevel*]

**rc-update** **show** [*runlevel*]

**rc-update** **update**

# DESCRIPTION

**rc-update** lets administrators add and remove services from a
runlevel using the OpenRC argv shape. Internally it is a thin shim
over **slinitctl**(8) — runlevels are not a native slinit concept, so
they are modelled as ordinary services named *runlevel-NAME* whose
**waits-for** dependencies enumerate the runlevel members.

**rc-update add nginx default** therefore becomes
**slinitctl --from runlevel-default enable nginx**, which writes a
**waits-for.d/** symlink under the runlevel service's description
directory. The change persists across reboots because slinit reads
the symlink on every load — there is no separate cache to rebuild.

# ACTIONS

**add** *service* [*runlevel*]
:   Add *service* to *runlevel*. If *runlevel* is omitted, defaults
    to **default**. Translates to:
    **slinitctl --from** *runlevel-NAME* **enable** *service*

**del**, **delete** *service* [*runlevel*]
:   Remove *service* from *runlevel*. Defaults to **default** when
    omitted. Translates to:
    **slinitctl --from** *runlevel-NAME* **disable** *service*

**show** [*runlevel*]
:   Print the dependency graph rooted at the runlevel. Translates to
    **slinitctl graph** *runlevel-NAME*. Defaults to **default**.

**update**, **-u**
:   No-op. Reports success and exits 0. OpenRC uses this verb to
    rebuild a boot-time dependency cache; slinit has no such cache
    (deps are resolved live from the service descriptions), so there
    is nothing to do.

Any verb not listed above is passed through to **slinitctl** verbatim,
so admins can discover what is supported.

# RUNLEVELS

The following OpenRC runlevel names are advertised by the family of
shims:

- **sysinit**
- **boot**
- **default**
- **nonetwork**
- **shutdown**

Each is just a service named *runlevel-NAME* in the slinit
service-description tree. Custom runlevels work the same way: define
your own **runlevel-myrunlevel** description and use it as the second
argument.

# ENVIRONMENT

**SLINITCTL**
:   Path to the **slinitctl** binary. Defaults to **slinitctl** (resolved
    against **PATH**).

# EXIT STATUS

The exit status of the underlying **slinitctl** invocation is returned
verbatim. Additionally:

**0**
:   Returned for **rc-update update** with no further action taken.

**127**
:   **SLINITCTL** could not be located on **PATH**.

**2**
:   Usage error (bad argv shape, invalid service or runlevel name).

# EXAMPLES

```
rc-update add nginx default
rc-update add cron boot
rc-update del legacyd default
rc-update show default
rc-update update             # no-op, returns 0
```

Define a custom runlevel by writing a service file
*/etc/slinit.d/runlevel-mymode* (type=internal) and use it like any
OpenRC runlevel:

```
rc-update add nginx mymode
```

# SEE ALSO

**slinit**(8), **slinitctl**(8), **rc-service**(8), **rc-status**(8),
**slinit-service**(5)

# AUTHORS

Ionut Nechita and contributors. slinit is licensed under Apache 2.0.
