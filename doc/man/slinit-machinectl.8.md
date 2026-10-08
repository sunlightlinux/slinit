% SLINIT-MACHINECTL(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-09-08

# NAME

slinit-machinectl - inspect and manage slinit's local container registry

# SYNOPSIS

**slinit-machinectl** *command* [*arguments*]

# DESCRIPTION

**slinit-machinectl** is the query/CRUD tool for the file-backed
container registry at */run/slinit/machines/*. Every container that
**slinit-nspawn**(8) launches writes one plain-text file into the
registry, named after the machine, holding its PID and optionally its
class, rootfs and owning service. **slinit-machinectl** reads and
mutates those files.

The registry is intentionally simple — one file per machine, all
plain text: the PID on the first line, then optional **CLASS=**,
**SERVICE=** and **ROOT=** lines. Scripts that prefer direct
filesystem access can read/write the files with **cat**(1) and
**sed**(1); this tool exists for interactive use, liveness probes,
and shell one-liners.

# COMMANDS

**register** *name* *pid* [**--class=**\ *class*] [**--service=**\ *svc*] [**--root=**\ *path*]
:   Create a registry entry named *name* pointing at *pid*. Optional
    metadata: *class* (typically **container**), *service* (the slinit
    service name that owns this machine, if any), *root* (rootfs path
    on the host; when empty, */proc/*\ *pid*\ */root* is used). An
    existing entry with the same name is overwritten. *name* is 1–64
    characters from **A–Z a–z 0–9 _ - .**, not starting with **.** or
    **-**; *pid* must be greater than 1.

**unregister** *name*
:   Remove the registry entry for *name*; removing an entry that does
    not exist succeeds. Does NOT kill the underlying process — use
    **slinitctl signal** or **kill**(1) for that.

**list**
:   Print a table with the columns NAME, PID, ALIVE, CLASS, SERVICE and
    ROOT. ALIVE is **yes** when the PID can still be signalled
    (*kill(pid, 0)*) and **no** otherwise; dead entries are not
    removed automatically (garbage-collection is the operator's
    responsibility).

**status** *name*
:   Show one machine's fields plus liveness in an aligned block.
    Suitable for direct terminal reading.

**show** *name*
:   Print the entry in registry-file form — the PID line followed by
    the non-empty **CLASS=**, **SERVICE=** and **ROOT=** lines.
    Suitable for scripts that want to parse it with their own tools.

# ENVIRONMENT

**SLINIT_MACHINES_DIR**
:   Override the registry directory (default */run/slinit/machines/*).
    Used by tests and rootless setups where writing to */run/* is not
    permitted.

# EXIT STATUS

**0**
:   Command completed successfully.

**1**
:   Error: no such machine (**status**, **show**), an invalid name or
    PID, a wrong number of arguments to a subcommand, an unknown option
    to **register**, permission denied on the registry directory, or a
    malformed registry file.

**2**
:   No subcommand, or an unknown one.

# FILES

*/run/slinit/machines/*\ *name*
:   Registry entry for one machine: the PID on line 1, then optional
    *KEY*=*VALUE* lines. Not a systemd .machine file — the format is
    deliberately smaller.

# EXAMPLES

Register an already-running container by PID (useful when an outer
harness launched the container without going through
**slinit-nspawn**):

    slinit-machinectl register alpine-svc 12345 \
        --class=container --root=/var/lib/machines/alpine-svc

List every registered machine and their liveness:

    slinit-machinectl list

Query one machine's state:

    slinit-machinectl status alpine-svc

Feed a registry entry into a script:

    slinit-machinectl show alpine-svc | head -n 1    # the PID

# SEE ALSO

**slinit-nspawn**(8), **slinit-journalctl**(8), **slinitctl**(8),
**slinit**(8)
