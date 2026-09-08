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
registry naming itself, its PID, its class, its rootfs, and the
service (if any) that owns it. **slinit-machinectl** reads and
mutates those files.

The registry is intentionally simple — one file per machine, all
plain text, one *key: value* per line. Scripts that prefer direct
filesystem access can read/write the files with **cat**(1) and
**sed**(1); this tool exists for interactive use, liveness probes,
and shell one-liners.

# COMMANDS

**register** *name* *pid* [**--class=**\ *class*] [**--service=**\ *svc*] [**--root=**\ *path*]
:   Create a registry entry named *name* pointing at *pid*. Optional
    metadata: *class* (typically **container**), *service* (the slinit
    service name that owns this machine, if any), *root* (rootfs path
    on the host). Fails if a live entry with the same name already
    exists.

**unregister** *name*
:   Remove the registry entry for *name*. Does NOT kill the underlying
    process — use **slinitctl signal** or **kill**(1) for that.

**list**
:   Print every registered machine as one line: *name*, *pid*, *class*,
    liveness. Liveness is derived from **/proc/**\ *pid* — an entry whose
    PID no longer exists is marked *(dead)* but is not auto-removed
    (garbage-collection is the operator's responsibility).

**status** *name*
:   Show one machine's fields plus liveness in an aligned block.
    Suitable for direct terminal reading.

**show** *name*
:   Print the raw registry file for *name* on stdout with no
    reformatting. Suitable for scripts that want to parse the file
    with their own tools.

# ENVIRONMENT

**SLINIT_MACHINES_DIR**
:   Override the registry directory (default */run/slinit/machines/*).
    Used by tests and rootless setups where writing to */run/* is not
    permitted.

# EXIT STATUS

**0**
:   Command completed successfully.

**1**
:   Registry-side error (no such machine, name collision on register,
    permission denied on the registry dir, malformed registry file).

**2**
:   Usage error (unknown subcommand, missing required argument,
    invalid PID).

# FILES

*/run/slinit/machines/*\ *name*
:   Registry entry for one machine. One *key: value* per line. Not
    a systemd .machine file — the format is deliberately smaller.

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

    slinit-machinectl show alpine-svc | awk -F': ' '/^Pid/ {print $2}'

# SEE ALSO

**slinit-nspawn**(8), **slinit-journalctl**(8), **slinitctl**(8),
**slinit**(8)
