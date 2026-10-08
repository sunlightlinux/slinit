% SLINIT-EINFO(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-07-21

# NAME

slinit-einfo, einfo, ewarn, eerror, ebegin, eend, ewend, esyslog,
ewaitfile, eval_ecolors - OpenRC-flavoured status output

# SYNOPSIS

**slinit-einfo** *MSG*...

*APPLET* [*ARGS*...]

where *APPLET* is a symlink named after any of the supported OpenRC
applets (see **APPLETS** below).

# DESCRIPTION

**slinit-einfo** is a drop-in replacement for OpenRC's einfo(1)
family. It dispatches via **basename**(**argv[0]**) so installers
ship a symlink per applet — `einfo`, `ewarn`, `eend`, and so on —
each pointing at the single **slinit-einfo** binary.

Each applet prints an OpenRC-style " * *MSG*" line, with the asterisk
coloured green (info), yellow (warning) or red (error), and, for the **ebegin** / **eend** pair,
a right-aligned `[ ok ]` or `[ !! ]` marker on column 80 (or
**$COLUMNS**). Colours are auto-detected from the target stream's
TTY-ness and can be forced off with **EINFO_COLOR=no**.

# APPLETS

**einfo** *MSG*
:   " * *MSG*" in green, stdout, newline.

**einfon** *MSG*
:   Same as **einfo** but no trailing newline.

**ewarn** *MSG*
:   " * *MSG*" in yellow, stderr, newline.

**ewarnn** *MSG*
:   Same as **ewarn**, no newline.

**eerror** *MSG*
:   " * *MSG*" in red, stderr, newline. Exits with **1**.

**eerrorn** *MSG*
:   Same as **eerror**, no newline.

**ebegin** *MSG*
:   " * *MSG* ..." in green, no newline. Sets up an **eend** on the
    same visual row.

**eend** [*STATUS* [*MSG*]]
:   Prints "[ ok ]" (green) when *STATUS* is 0 or omitted, "[ !! ]"
    (red) otherwise. If *MSG* is given it is printed first, as an info
    line when *STATUS* is 0 and as an error line otherwise. Propagates
    *STATUS* as the exit code; a *STATUS* that is not an integer is an
    error (exit 1).

**ewend** *STATUS* [*MSG*]
:   Like **eend** but uses the yellow warning palette for non-zero
    *STATUS* (advisory end, not fatal).

**veinfo**, **veinfon**, **vewarn**, **vewarnn**, **vebegin**, **veend**, **vewend**
:   Verbose variants — only emit output when **EINFO_VERBOSE** is
    truthy (**1**, **y**, **yes**, **true** or **on**). Otherwise they
    print nothing; **veend** and **vewend** still return *STATUS*.

**eindent**, **eoutdent**, **veindent**, **veoutdent**
:   No-op stubs — indent tracking requires mutating **EINFO_INDENT**
    in the parent shell, which a subprocess cannot do. Init.d
    wrappers manage the variable themselves; downstream applets
    honour whatever level they find.

**esyslog** *LEVEL*[.**FACILITY**] *TAG* *MSG*...

**elog** *LEVEL*[.**FACILITY**] *TAG* *MSG*...
:   Emit *MSG* via **syslog**(3) with the given priority. *LEVEL*
    accepts syslog severity names (**info**, **notice**, **warning**,
    **err**, **crit**, **alert**, **emerg**, **debug**) or a numeric
    priority. *FACILITY* defaults to **user** and accepts the standard
    names (**daemon**, **auth**, **cron**, **local0**-**local7**, …).
    Falls back to printing "*TAG*: *MSG*" on stderr when **/dev/log** is
    unreachable. Fewer than three arguments, or an unknown severity or
    facility, is an error (exit 1).

**ewaitfile** *TIMEOUT* *PATH*...
:   Wait for each *PATH* in turn to exist, polling every 20 ms.
    *TIMEOUT* is in seconds and applies to each path separately; **0**
    or a negative value means wait forever. The first path that times
    out ends the command with exit 1. When **EINFO_VERBOSE** is set, a
    "Waiting for *PATH*" line is framed by an **eend** marker on
    success or an **ewend** marker on timeout.

**eval_ecolors**
:   Print the current colour palette as **KEY='value'** shell
    assignments so scripts can `eval $(eval_ecolors)` to pull the
    escape codes into their environment.

# ENVIRONMENT

**EINFO_QUIET**
:   Truthy value (**1**, **y**, **yes**, **true**, **on**) suppresses
    every applet's output. Exit codes are unaffected.

**EINFO_VERBOSE**
:   Truthy value enables the **v** variants (**veinfo**, etc.) and
    the progress lines in **ewaitfile**.

**EINFO_COLOR**
:   Set to **no** to force plain output even when the target is a
    TTY. Colours are also disabled when the stream is not a TTY or
    when **TERM** is empty / **dumb**.

**EINFO_INDENT**
:   Non-negative integer; leading spaces are prepended to every
    " * *MSG*" line. Clamped to 40 to keep pathological values
    from wrapping the terminal.

**COLUMNS**
:   Terminal width used to right-align the **[ ok ]** / **[ !! ]**
    marker. Defaults to 80.

**TERM**
:   Empty or **dumb** disables colours.

# EXIT STATUS

- **eerror** / **eerrorn**: always **1**.
- **eend** / **ewend** (and their **v** variants): the *STATUS*
  argument, **0** when it is omitted, **1** when it is not an integer.
- **ewaitfile**: **0** if every path appeared before its timeout; **1**
  otherwise, or on a usage error.
- **esyslog** / **elog**: **0**, including when syslog is unreachable;
  **1** on a usage error.
- An applet name that is not recognised: **1**.
- Everything else: **0**.

# EXAMPLES

Info line with indent:

```
EINFO_INDENT=2 einfo "Bringing up eth0"
```

Progress pair:

```
ebegin "Loading kernel modules"
modprobe -a i915 nvme
eend $?
```

Wait up to 10 seconds for /run/foo.sock to appear:

```
ewaitfile 10 /run/foo.sock
```

Syslog a warning line to the daemon facility:

```
esyslog warning.daemon myservice "config file was regenerated"
```

# SEE ALSO

**einfo**(1) (OpenRC), **syslog**(3), **slinit**(8),
**slinit-fstabinfo**(8), **slinit-mountinfo**(8).
