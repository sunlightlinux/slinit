# NAME

slinit-checkpath - create or verify filesystem paths with type, mode,
and ownership

# SYNOPSIS

**slinit-checkpath** [**-d** | **-D** | **-f** | **-F** | **-p**]
[**-m** *MODE*] [**-o** *USER*[**:***GROUP*]] [**-W**] *PATH*...

# DESCRIPTION

**slinit-checkpath** ensures that one or more filesystem paths exist
with the requested type, mode, and ownership. It is the slinit
equivalent of OpenRC's **checkpath**(8) helper, intended for use from
service **pre-start** commands and from scripts that want a single
idempotent step to "make sure */run/foo* exists, is owned by *bar*,
and is mode 0755".

The flags are modelled directly after OpenRC's **checkpath**, so most
existing init-script idioms transfer unchanged.

# OPTIONS

## Path type (mutually exclusive)

At most one of the following may be set. Omitting all of them is legal
when only **-W** is requested (writable check, no creation).

**-d**, **--directory**
:   Ensure *PATH* is a directory; create it (and any missing parents)
    if necessary.

**-D**, **--directory-truncate**
:   As **-d**, but also truncates the directory by removing every entry
    inside it. Useful for rebuilding stale runtime state at boot.

**-f**, **--file**
:   Ensure *PATH* is a regular file; create it if missing.

**-F**, **--file-truncate**
:   As **-f**, but truncates the file to zero length on every run.

**-p**, **--pipe**
:   Ensure *PATH* is a named pipe (FIFO); create it via **mkfifo**(3)
    if missing.

## Attributes

**-m**, **--mode** *MODE*
:   Desired permission mode for *PATH*. Accepts the usual octal
    spelling (**0755**, **0644**, etc.). When set, the path is
    **chmod**(2)-ed to the mode after type creation.

**-o**, **--owner** *USER*[**:***GROUP*]
:   Desired ownership. *USER* and *GROUP* may each be a name or a
    numeric ID. The colon-separated form sets both; a bare *USER*
    leaves the group untouched.

**-W**, **--writable**
:   Treat the call as a success when *PATH* is already writable, even
    if the type/mode/owner do not match. Useful for "fix it if you
    can, otherwise let me decide" patterns in pre-start hooks.

# EXIT STATUS

**0**
:   Every *PATH* satisfied the requested constraints (creating,
    truncating, or adjusting them as needed).

**1**
:   At least one *PATH* could not be made to satisfy the constraints.
    The first such failure is reported on stderr and execution
    continues for the remaining paths so the operator can see all
    the work needed; the exit status remains nonzero.

# EXAMPLES

Typical use from a slinit service description's **pre-start**:

```
pre-start = /usr/bin/slinit-checkpath \
                -d -m 0755 -o redis:redis /run/redis
```

Ensure a runtime FIFO exists with restrictive perms:

```
slinit-checkpath -p -m 0600 -o root:root /run/myapp.fifo
```

Truncate a stale directory at boot:

```
slinit-checkpath -D /run/lock
```

# SEE ALSO

**slinit**(8), **slinitctl**(8), **slinit-service**(5),
**chmod**(1), **chown**(1), **mkfifo**(1)

# AUTHORS

Ionut Nechita and contributors. slinit is licensed under Apache 2.0.
