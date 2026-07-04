# NAME

slinit-sysctl - apply sysctl.d(5) tunables to /proc/sys
(systemd-sysctl clone)

# SYNOPSIS

**slinit-sysctl** [*OPTIONS*] [*FILE*...]

# DESCRIPTION

**slinit-sysctl** is a drop-in replacement for **systemd-sysctl**(1).
It reads **sysctl.d**(5) configuration files, applies each
**key = value** assignment to the matching entry under **/proc/sys**,
and reports any failures. It is intended to be invoked once during
early boot so that the tunables distributions ship under
**/usr/lib/sysctl.d/** (and administrators override under
**/etc/sysctl.d/**) actually take effect.

Without positional arguments the tool scans, in order:

- **/usr/lib/sysctl.d**
- **/usr/local/lib/sysctl.d**
- **/run/sysctl.d**
- **/etc/sysctl.d**

then appends **/etc/sysctl.conf** if it exists. Same-basename
collisions across the .d directories resolve to the later directory
so an operator override at **/etc/sysctl.d/foo.conf** always beats
**/usr/lib/sysctl.d/foo.conf**.

# CONFIG FORMAT

Each line is either blank, a comment (starts with **#** or **;**
after any leading whitespace), or a **key = value** assignment.

The **key** may use **.** or **/** as its separator; both forms
resolve to the same **/proc/sys/…** path. A leading **-** on the key
marks the assignment as best-effort: a failed write (missing tunable,
kernel refuses the value) is counted separately and does not fail
the pass.

Examples:

```
# Enable IP forwarding.
net.ipv4.ip_forward = 1

# Multi-value tunables preserve internal whitespace.
kernel.printk = 4 4 1 7

# Best-effort: skip silently if the tunable is absent.
-vm.swappiness = 60
```

Wildcards (**\***, **?**) in keys are **not** supported and reject the
line — v1 does not expand them.

# OPTIONS

**-s**, **--strict**
:   Disregard the leading **-** on keys. Every failed write becomes
    an error. Useful for auditing a config against the current kernel.

**-v**, **--verbose**
:   Emit a one-line summary (**applied=N ignored=M errors=E**) plus
    a line per skipped write to stderr.

**--root** *DIR*
:   Prefix *DIR* onto every hardcoded path — the four scan roots,
    the legacy /etc/sysctl.conf, and **/proc/sys**. Useful for
    previewing a config in a fixture tree; never needed in production.

**-h**, **--help**
:   Print usage.

**-V**, **--version**
:   Print version string.

# EXIT STATUS

- **0**: every assignment applied cleanly.
- **1**: at least one non-ignored write failed (or a config file was
  malformed).
- **2**: bad usage / unknown flag.

# EXAMPLES

Apply everything on boot:

```
slinit-sysctl
```

Audit a config file without touching **/proc**:

```
slinit-sysctl --root=/tmp/preview /etc/sysctl.d/99-mine.conf
```

Enforce a config strictly (fail on any missing tunable):

```
slinit-sysctl --strict --verbose
```

# SEE ALSO

**sysctl**(8), **sysctl.conf**(5), **sysctl.d**(5), **proc**(5),
**systemd-sysctl**(1), **slinit**(8), **slinit-binfmt**(8).
