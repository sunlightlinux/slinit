% SLINIT-SYSCTL(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-07-21

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
**/usr/lib/sysctl.d/foo.conf**. Only files ending in **.conf** are read; they
are applied in alphabetical order of their basenames, then
**/etc/sysctl.conf** last.

# CONFIG FORMAT

Each line is either blank, a comment (starts with **#** or **;**
after any leading whitespace), or a **key = value** assignment.

The **key** may use **.** or **/** as its separator; both forms
resolve to the same **/proc/sys/…** path. As in **sysctl.d**(5), if
the first separator is **.**, dots and slashes are swapped; if it is
**/**, the key is used as is. So a dot inside a name — the VLAN
interface *eth0.100* — is written **net.ipv4.conf.eth0/100.rp_filter**
or **net/ipv4/conf/eth0.100/rp_filter**. A leading **-** on the key
marks the assignment as best-effort: a failed write (kernel refuses
the value, missing tunable) is counted as ignored and does not fail
the pass.

The **key** may be a glob (**\***, **?**, **[...]**), in either
separator spelling. It is expanded against **/proc/sys** and the value
is written to every match. A line **-key** with no **=** is an
exclusion: globs skip that key. A key that has its own assignment
anywhere in the configuration is skipped by every glob too, whatever
the order of the lines, so an explicit setting always wins over a
glob. As in **systemd-sysctl**, every file feeds one set of
settings: a later assignment of a key replaces an earlier one, and a
later **-key** cancels an earlier assignment of that key.

Examples:

```
# Enable IP forwarding.
net.ipv4.ip_forward = 1

# Multi-value tunables preserve internal whitespace.
kernel.printk = 4 4 1 7

# Best-effort: skip silently if the write fails.
-vm.swappiness = 60

# Every interface but "all" gets loose reverse-path filtering.
net.ipv4.conf.*.rp_filter = 2
-net.ipv4.conf.all.rp_filter
```

A line that cannot be parsed (no **=** and no leading **-**, empty
key, a **.** or **..** element, a leading **/**) is reported with its
file and line and skipped; the rest of the file still applies, and the
pass exits 1, as **systemd-sysctl** does.

A tunable the running kernel does not have (ENOENT), including a glob
that matches nothing, is ignored with a warning on stderr. A write the
kernel refuses for permission (EROFS, EACCES, EPERM — how container
managers protect sysctls) is ignored silently (shown under
**--verbose**). **--strict** turns both back into errors.

# OPTIONS

**-s**, **--strict**
:   Every failed write is an error: a missing tunable, a refused
    write, and — unlike **systemd-sysctl**, where **-** still wins —
    a write to a **-**-prefixed key. Useful for auditing a config
    against the current kernel.

**-v**, **--verbose**
:   Emit a one-line summary (**applied=N ignored=M errors=E**) plus
    a line per skipped write to stderr.

**--root** *DIR*
:   Prefix *DIR* onto every hardcoded path — the four scan roots,
    the legacy /etc/sysctl.conf, and **/proc/sys**. Values are then
    written as files under *DIR*/proc/sys (whose directories must
    exist; a missing file is created, which never happens against the
    real **/proc/sys**). Globs expand against the files already
    there. Useful for previewing a config in a fixture tree; never
    needed in production. *FILE* arguments are not prefixed.

**-h**, **--help**
:   Print usage.

**-V**, **--version**
:   Print version string.

# EXIT STATUS

- **0**: every assignment applied cleanly.
- **1**: at least one non-ignored write failed, a config file could
  not be opened, or a line could not be parsed (the other lines were
  still applied).
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
