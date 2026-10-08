% SLINIT-CGTOP(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-07-19

# NAME

slinit-cgtop - top-like viewer for cgroup v2 resource consumption

# SYNOPSIS

**slinit-cgtop** [**\--delay** *DURATION*] [**\--iterations** *N*]
                  [**\--depth** *N*] [**\--sort** *KEY*]
                  [**\--once**] [**\--all**]

# DESCRIPTION

**slinit-cgtop** walks the cgroup v2 hierarchy under */sys/fs/cgroup*
and prints a periodically-refreshed table of per-cgroup process count
(lines of **cgroup.procs**), CPU%, memory (**memory.current**, which
includes page cache) and IO read/write bytes per second. It is the **systemd-cgtop**(1)
equivalent, scoped to what a slinit-managed system needs:

- **cgroup v2 only**. Modern systemd-cgtop (v250+) is v2-only for
  the same reason — the v1 per-controller layout does not expose the
  unified accounting knobs (**memory.current**, **cpu.stat**,
  **io.stat**) this tool reads. On a v1 host, **slinit-cgtop**
  says so and names the kernel command-line flag that switches to v2.

- **Rate-based fields** (CPU%, IO bytes/sec) are computed as a
  delta over the sampling interval, so a cold snapshot lands in one
  **\--delay** cycle.

- **kernfs virtual files** always report `st_size = 0`; the tool
  counts lines in **cgroup.procs** instead of relying on stat.

- **Idle-cgroup filter** by default hides cgroups with 0 processes +
  0 memory + 0% CPU so the output stays scannable. **\--all** disables
  the filter for capacity planning runs.

Typical use is triage: "which service is eating the machine right
now" without opening a full monitoring stack.

# OPTIONS

**\--delay** *DURATION*
:   Refresh interval. Accepts any duration parseable by
    **time.ParseDuration** (*1s*, *200ms*, *2m*). Default: *1s*.

**\--iterations** *N*
:   Number of refreshes before exiting. *0* (default) = infinite.
    Combined with **\--delay**, useful for scripted sampling
    (e.g. **\--iterations=5 \--delay=2s** for a 10-second snapshot).

**\--depth** *N*
:   Maximum cgroup tree depth from the root. Default: *3*. Prevents
    a systemd-managed graph with hundreds of nested scope/slice/
    service entries from filling the screen. Root + slice + unit is
    usually enough.

**\--sort** *KEY*
:   Sort column: *cpu* (default), *mem* (or *memory*), *tasks*, or
    *path*. Any other value sorts by *cpu*.

**\--once**
:   Take exactly one delta snapshot and exit. Script-friendly. The
    tool blocks for **\--delay** to gather the delta, prints one
    table, and exits.

**\--all**
:   Include idle cgroups (0 processes, 0 memory, 0% CPU). Off by default so
    the output focuses on active workloads.

# EXAMPLES

Live view refreshing every second, sorted by CPU:

    slinit-cgtop

One 2-second snapshot of the top memory users, script-friendly
output:

    slinit-cgtop --once --delay=2s --sort=mem

Capacity-planning snapshot of the entire tree, including idle
cgroups:

    slinit-cgtop --once --all --depth=10

# EXIT STATUS

**0**
:   Normal termination: SIGINT (Ctrl-C), SIGTERM, **\--once**, or
    after **\--iterations** rounds.

**1**
:   Host is running cgroup v1 (the diagnostic suggests booting with
    *systemd.unified_cgroup_hierarchy=1*), cgroup v2 is not mounted at
    */sys/fs/cgroup*, or a scan of the tree failed.

**2**
:   Bad option.

# SEE ALSO

**slinit**(8), **slinit-service**(5), **systemd-cgtop**(1),
**cgroups**(7)
