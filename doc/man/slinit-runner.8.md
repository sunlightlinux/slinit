# NAME

slinit-runner - exec helper that applies mlockall(2) and set_mempolicy(2)
before running a service

# SYNOPSIS

**slinit-runner** [**\--mlockall**=*N*] [**\--mempolicy**=*MODE*]
[**\--numa-nodes**=*LIST*] **\--** *COMMAND* [*ARGS*...]

# DESCRIPTION

**slinit-runner** is a small exec helper invoked by **slinit**(8) when
a service description sets **mlockall** or **numa-mempolicy**. It
exists because **mlockall**(2) and **set_mempolicy**(2) operate on
the *calling* process — slinit cannot apply them to a freshly
**fork**(2)ed child remotely, the way it does for **sched_setattr**(2).

The helper:

1. Parses the flags below.
2. Calls **set_mempolicy**(2) and/or **mlockall**(2) on itself.
3. Replaces itself with *COMMAND* via **execve**(2).

After **execve**(2) the running process is the real service binary,
not slinit-runner — its PID matches what **slinitctl status** reports
and signals reach the right place.

slinit-runner is not intended for direct human use; the daemon
synthesises invocations from the **mlockall** / **numa-mempolicy** /
**numa-nodes** keys in **slinit-service**(5).

# OPTIONS

**\--mlockall**=*N*
:   Bitmask passed to **mlockall**(2). The flag values follow
    **<sys/mman.h>**: **MCL_CURRENT**=1, **MCL_FUTURE**=2,
    **MCL_ONFAULT**=4. The slinit daemon translates the symbolic
    config keywords (*current*, *future*, *both*, *onfault*) into the
    numeric mask before invoking the helper.

**\--mempolicy**=*MODE*
:   NUMA memory allocation mode for **set_mempolicy**(2). One of
    **bind**, **preferred**, **interleave**, **local**, **default**.

**\--numa-nodes**=*LIST*
:   Node mask for **bind** / **preferred** / **interleave**. Accepts
    comma-separated singles and hyphen ranges (e.g. *0-3* or
    *0,2,4*). Rejected for *local* and *default*.

# EXIT STATUS

**slinit-runner** does not normally return: on success it is replaced
by the target program. Failures of the helper itself produce:

**2**
:   Bad command line, unknown mempolicy mode, or **set_mempolicy**(2)
    / **mlockall**(2) failed (typically EPERM without
    **CAP_IPC_LOCK** / **CAP_SYS_NICE**).

If **execve**(2) fails after the syscalls succeeded the locked memory
is released when the helper exits.

# SEE ALSO

**slinit**(8), **slinit-service**(5),
**mlockall**(2), **set_mempolicy**(2), **numa**(7)

# AUTHORS

Ionut Nechita and contributors. slinit is licensed under Apache 2.0.
