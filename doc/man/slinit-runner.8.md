# slinit-runner 8 "" "" "slinit \- service management system"

## NAME

slinit-runner - exec helper that applies per-service post-fork setup
(sandbox, seccomp, capabilities, NUMA, AppArmor) before running a
service

## SYNOPSIS

**slinit-runner** [*setup-options*] **\--** *COMMAND* [*ARGS*...]

## DESCRIPTION

**slinit-runner** is a small `execve`-style helper invoked by
**slinit**(8) when a service description sets any option that must be
applied to the *calling* process rather than to the parent. Examples:

- **mlockall**(2), **set_mempolicy**(2) — affect the caller's own
  memory policy, so slinit cannot apply them to a fork()ed child from
  the parent side.
- Filesystem sandbox mounts (**private-tmp**, **protect-system**,
  bind-paths, tmpfs-paths, …) — require the child's own mount
  namespace and must be set up before **execve**.
- Seccomp filters, capability sets, ambient caps, **PR_SET_NO_NEW_PRIVS**,
  **PR_CAPBSET_DROP** — attach to the current task and are inherited
  through **execve**.
- AppArmor `aa_change_onexec` — kernel binds the profile transition
  to the task that performs the exec.

The helper:

1. Parses the flags below.
2. Applies each active setup step in a defined order (sandbox mounts
   → capability bounding → seccomp → run-as UID/GID drop → ambient
   caps restore → **PR_SET_NO_NEW_PRIVS** → AppArmor `exec` label).
3. Replaces itself with *COMMAND* via **execve**(2).

After **execve**(2) the running process is the real service binary,
not slinit-runner — its PID matches what **slinitctl status** reports
and signals reach the right place.

slinit-runner is not intended for direct human use; the daemon
synthesises invocations from the corresponding keys in
**slinit-service**(5). The flag names below map 1:1 to those keys.

## OPTIONS

### NUMA & memory locking

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

### AppArmor & debugging

**\--apparmor**=*PROFILE*
:   AppArmor profile to transition into on the upcoming **execve**.
    slinit-runner writes `exec` *PROFILE* to `/proc/self/attr/exec`
    just before exec; the kernel binds the transition to the task
    that performs the exec, which is why this must happen in the
    child, not in the parent. Requires the AppArmor LSM to be active.

**\--debug**
:   Raise **SIGSTOP** before **execve** so a debugger can attach.
    Resume with **SIGCONT**. Intended for developer use only.

### Filesystem sandbox

**\--private-tmp**
:   Mount a fresh **tmpfs** at `/tmp` and `/var/tmp`. Mirrors systemd
    **PrivateTmp=**.

**\--protect-system**=*MODE*
:   Remount system paths read-only. *MODE* is one of **yes**,
    **full**, or **strict**. Mirrors systemd **ProtectSystem=**.

**\--protect-home**=*MODE*
:   Hide `/home`, `/root`, `/run/user`. *MODE* is one of **yes**,
    **read-only**, or **tmpfs**. Mirrors systemd **ProtectHome=**.

**\--protect-proc**=*MODE*
:   `/proc` **hidepid=** mode. *MODE* is one of **noaccess**,
    **invisible**, or **ptraceable**. Mirrors systemd **ProtectProc=**.

**\--proc-subset**=*MODE*
:   `/proc` **subset=** filter. Currently supports **pid**. Mirrors
    systemd **ProcSubset=**.

**\--read-only-path**=*PATH*
:   Bind-mount *PATH* read-only over itself. Repeatable.

**\--read-write-path**=*PATH*
:   Keep *PATH* writable even when **\--protect-system**=full/strict
    would make it read-only. Repeatable.

**\--inaccessible-path**=*PATH*
:   Hide *PATH* behind an empty inaccessible mount. Repeatable.

**\--bind-path**=*SRC*:*DST*
:   Writable bind-mount *SRC* onto *DST*. Repeatable.

**\--bind-ro-path**=*SRC*:*DST*
:   Read-only bind-mount *SRC* onto *DST*. Repeatable.

**\--tmpfs-path**=*PATH*[:*OPTIONS*]
:   Mount a fresh **tmpfs** at *PATH*. *OPTIONS* is a comma-separated
    `mount(8)`-style list (e.g. `size=64M,mode=0700`). Repeatable.

### Seccomp

**\--syscall-action**=*ACTION*
:   Default action for non-allowed syscalls. One of **kill**, **log**,
    **trap**, `errno-`*NAME*, or `errno-`*NUMBER*.

**\--syscall-filter**=*ITEM*
:   Add a seccomp filter item: a syscall name, an **@**-prefixed
    curated group (e.g. `@system-service`, `@privileged`, `@debug`),
    or **~**-prefixed drop item as the first entry to switch the
    list from allowlist to denylist. Repeatable.

**\--syscall-arch**=*ARCH*
:   Additional accepted architecture for the seccomp filter (e.g.
    `native`, `x86-64`, `x86`, `arm64`, `arm`). Repeatable.

**\--syscall-log**=*ITEM*
:   Add a syscall (or **@**-group) that is always logged via
    **SECCOMP_RET_LOG**, regardless of the allow/deny decision.
    Repeatable.

### Restrict/Protect hardening cluster

Each flag below is a bool. Actives are combined into a deny-mode
seccomp filter plus a small set of mount operations applied before
**execve**.

**\--protect-kernel-tunables**
:   Block writes to `/proc/sys` and `iopl`/`ioperm`/`swapon`
    syscalls. Mirrors systemd **ProtectKernelTunables=**.

**\--protect-kernel-modules**
:   Block **init_module**(2) / **finit_module**(2) /
    **delete_module**(2). Mirrors **ProtectKernelModules=**.

**\--protect-kernel-logs**
:   Block **syslog**(2) and hide `/dev/kmsg`. Mirrors
    **ProtectKernelLogs=**.

**\--protect-clock**
:   Block **clock_settime**(2) / **adjtime**(3) /
    **settimeofday**(2) / **adjtimex**(2). Mirrors
    **ProtectClock=**.

**\--protect-control-groups**
:   Remount `/sys/fs/cgroup` read-only. Mirrors
    **ProtectControlGroups=**.

**\--protect-hostname**
:   Block **sethostname**(2) / **setdomainname**(2). Mirrors
    **ProtectHostname=**.

**\--lock-personality**
:   Block **personality**(2). Mirrors **LockPersonality=**.

### Credentials & capabilities

The runner stays root through the setup phase because mount and
seccomp operations require **CAP_SYS_ADMIN**, then drops to the
requested UID/GID just before exec. Ambient caps are restored after
the UID change via **PR_SET_KEEPCAPS** + per-cap
**PR_CAP_AMBIENT_RAISE**, because the kernel clears the ambient set
on UID change otherwise.

**\--run-as-uid**=*UID*
:   Drop to *UID* just before **execve**. **-1** disables the drop.

**\--run-as-gid**=*GID*
:   Drop to *GID* just before **execve**. **-1** disables the drop.

**\--ambient-cap**=*NUM*
:   Capability number to raise in the ambient set after the run-as
    drop (see **capabilities**(7)). Repeatable.

**\--bounding-cap**=*NUM*
:   Capability number to retain in **CapBnd**; every other capability
    is dropped via **PR_CAPBSET_DROP**. Repeatable. When any
    **\--bounding-cap** is passed the runner switches to positive-list
    mode — only the listed caps survive.

### Prctl

**\--no-new-privs**
:   Set **PR_SET_NO_NEW_PRIVS** before **execve**. Once set, the
    kernel refuses to grant new privileges via setuid, capabilities,
    or file-capabilities for the process and every subsequent
    execve. Mirrors dinit's *options=no-new-privs*.

## EXIT STATUS

**slinit-runner** does not normally return: on success it is replaced
by the target program. Failures of the helper itself produce:

**2**
:   Bad command line, unknown mode/action, or a required syscall
    (**set_mempolicy**, **mlockall**, **mount**, **seccomp**,
    **prctl**, **setresuid**, …) failed. Typical causes: missing
    **CAP_SYS_ADMIN** / **CAP_IPC_LOCK** / **CAP_SYS_NICE**, invalid
    mount target, unknown seccomp group, invalid AppArmor profile.

If **execve**(2) fails after the syscalls succeeded the locked
memory and mount state are released when the helper exits.

## SEE ALSO

**slinit**(8), **slinit-service**(5),
**capabilities**(7), **seccomp**(2), **prctl**(2),
**mount**(2), **mlockall**(2), **set_mempolicy**(2), **numa**(7)

## AUTHORS

Ionut Nechita and contributors. slinit is licensed under Apache 2.0.
