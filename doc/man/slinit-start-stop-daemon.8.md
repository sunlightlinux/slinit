# NAME

slinit-start-stop-daemon - start or stop system daemons (OpenRC-compatible)

# SYNOPSIS

**slinit-start-stop-daemon** [*OPTIONS*] {**--start** | **--stop** | **--status**} [**--** *ARGS*...]

# DESCRIPTION

**slinit-start-stop-daemon** is a drop-in replacement for the OpenRC and
Debian **start-stop-daemon**(8) utility. It exists so that ported
**/etc/init.d** scripts — which typically hard-code
`start-stop-daemon --start --exec …` and `start-stop-daemon --stop
--pidfile …` — keep working under **slinit**(8) without rewrite.

The tool is standalone. It performs no coordination with the running
**slinit** daemon; it only forks the requested process (for **--start**)
or signals matching processes (for **--stop** and **--status**). For
production services, define a real slinit service description under
**/etc/slinit.d** instead — that gives you supervision, dependencies,
restart, and hardening.

# MODES

**-S**, **--start**
:   Fork the requested binary. Refuses to start if a matching process
    is already running (see **PROCESS MATCHING** below).

**-K**, **--stop**
:   Send a signal to every matching process, optionally following an
    escalation schedule.

**--status**
:   Query whether a matching process is running. Prints matched PIDs
    on stdout and exits **0** if any were found.

# PROCESS MATCHING

At least one of **--exec**, **--pidfile**, or **--name** must be given.
When multiple are provided, a process must satisfy **all** of them.

**-x**, **--exec** *PATH*
:   Match processes whose **/proc/PID/exe** symlink resolves to *PATH*.
    Also names the binary to spawn on **--start**.

**-p**, **--pidfile** *PATH*
:   Read PID from *PATH*. When set on **--start**, combine with
    **--make-pidfile** to write it after fork. Empty/malformed files
    are treated as "not running".

**-n**, **--name** *NAME*
:   Match against **/proc/PID/comm** (the kernel task name, truncated
    to 15 bytes).

**-u**, **--user** *USER*
:   Match processes owned by *USER* (name or uid). On **--start** this
    also sets the child credentials unless **--chuid** is given.

# START-ONLY OPTIONS

**-a**, **--startas** *PATH*
:   Use *PATH* as the binary to execute, keeping **--exec** as the
    argv[0] presented to the child (Debian convention).

**-c**, **--chuid** *USER*[:*GROUP*]
:   Set the child user (and optionally group). Overrides **--user**
    for credential selection.

**-g**, **--group** *GROUP*
:   Set the child's primary group.

**-d**, **--chdir** *DIR*
:   Change to *DIR* before **exec**(2).

**-r**, **--chroot** *DIR*
:   Chroot into *DIR* before **exec**(2). Requires **CAP_SYS_CHROOT**.

**-b**, **--background**
:   Fork the child into a new session (**setsid**(2)) and return
    immediately. The parent reaps the child asynchronously so the
    caller sees no zombie.

**-m**, **--make-pidfile**
:   After the child starts, write its PID to the path from
    **--pidfile**. A no-op without **--pidfile**.

**-N**, **--nicelevel** *N*
:   **setpriority**(2) applied post-fork.

**--oom-score-adj** *N*
:   Write /proc/*PID*/oom_score_adj. Value range **-1000** to **1000**.

**-k**, **--umask** *OCT*
:   Set the process umask (octal, e.g. **022**).

**-0**, **--stdin** *PATH*
:   Redirect stdin from *PATH*.

**-1**, **--stdout** *PATH*
:   Redirect stdout to *PATH* (append mode).

**-2**, **--stderr** *PATH*
:   Redirect stderr to *PATH* (append mode).

**-e**, **--env** *KEY*=*VAL*
:   Append an environment entry. Repeatable.

**-I**, **--ionice** *CLASS*[:*LEVEL*]
:   Set **ioprio_set**(2). *CLASS* is one of **rt**/**realtime** (1),
    **be**/**best-effort** (2), or **idle** (3). *LEVEL* is 0-7.

**-w**, **--wait** *MS*
:   Sleep *MS* milliseconds after starting the child (readiness fudge).
    Also sets the timeout for **--notify readiness=pidfile**.

# HARDENING (RUNNER-WRAPPED)

The following flags require **slinit-runner**(8) on **PATH** or next to
the **slinit-start-stop-daemon** binary. They are applied inside the
child before **exec**(2) — a peer task cannot set them from outside.

**--capabilities** *LIST*
:   Comma- or space-separated list of capabilities to raise in the
    ambient set (and retain in the bounding set). Accepts kernel names
    (**cap_net_bind_service**, **cap_sys_admin**, …) or numeric bits.

**--secbits** *BITS*
:   **PR_SET_SECUREBITS** bitmask. Names (**keep_caps**,
    **no_setuid_fixup**, **noroot**, …) are parsed via
    **pkg/process.ParseSecurebits**.

**--no-new-privs**
:   Set **PR_SET_NO_NEW_PRIVS**(2) before **exec**(2).

# REAL-TIME SCHEDULING

**--scheduler** *POLICY*
:   Apply **sched_setattr**(2) on the child. *POLICY* is one of
    **other**/**normal**, **fifo**, **rr**, **batch**, **idle**.

**--scheduler-priority** *N*
:   Priority for **fifo** and **rr** policies (1-99 for RT). Ignored by
    other policies.

# LOGGING & READINESS

**-3**, **--stdout-logger** *CMD*
:   Pipe the child's stdout to *CMD*'s stdin. *CMD* is
    whitespace-split; no shell interpolation.

**-4**, **--stderr-logger** *CMD*
:   Same for the child's stderr.

**--notify** **readiness=**{**none**|**pidfile**}
:   Readiness protocol. **none** returns immediately after fork.
    **pidfile** blocks until **--pidfile** appears (bounded by
    **--wait** milliseconds, or 30s if not set).

**-P**, **--progress**
:   Print a "." to stderr each second while sleeping (during **--wait**,
    **--retry** escalation, or **--notify readiness=pidfile** polls).

**-i**, **--interpreted**
:   When matching by **--name** or **--exec**, follow the interpreter:
    if **/proc/PID/exe** resolves to a shell, python, perl, etc., the
    match target becomes **argv[1]** from **/proc/PID/cmdline**
    (typical shape for daemons launched as `sh /path/to/script`).

# STOP-ONLY OPTIONS

**-s**, **--signal** *SIG*
:   Signal to send. Name (**TERM**, **SIGTERM**) or number. Default
    **SIGTERM**.

**-R**, **--retry** *SPEC*
:   Escalation schedule. Two forms:

    - Integer seconds: **--retry 5** is shorthand for
      **--retry TERM/5/KILL/5**.
    - Slash-separated: **--retry TERM/30/KILL/5** sends SIGTERM, waits
      up to 30s, then sends SIGKILL and waits up to 5s.

    A trailing signal without timeout means "wait forever" for that
    step.

# COMMON OPTIONS

**-t**, **--test**
:   Print what would happen and exit **0** without spawning or
    signalling anything.

**-q**, **--quiet**
:   Suppress non-error output.

**-v**, **--verbose**
:   Print extra diagnostics on stderr.

**-o**, **--oknodo**
:   Return **0** instead of **1** when the operation was a no-op
    (already running for **--start**, not running for **--stop**).

# EXIT STATUS

Following the Debian/LSB convention:

- **0**: success
- **1**: already running (for **--start**) or not running (for **--status**)
- **2**: syntax / bad usage
- **3**: unimplemented feature or **--status** target not found
- **4**: insufficient privileges or spawn failure
- **5**: **--stop** invoked with a **--pidfile** whose target is gone
  (stale pidfile). Use **--oknodo** to reduce this to **0**.

# NOT IMPLEMENTED

The following niche modes remain unsupported:

- **--notify readiness=**{**fd:**N|**stderr**|**signal**|**manual**} —
  only **none** and **pidfile** are wired.
- The **-a**/**--startas** override is silently ignored when hardening
  flags are present, because **slinit-runner** re-execs with its own
  positional as **argv[0]**. Use a native slinit service description
  when both are needed.

For deeper hardening (seccomp filters, sandbox paths, cgroup limits,
NUMA policy, mlockall, AppArmor) use a native slinit service
description — see **slinit-service**(5).

# EXAMPLES

Start nginx as a system daemon:

```
slinit-start-stop-daemon --start \\
    --pidfile /run/nginx.pid \\
    --make-pidfile --background \\
    --exec /usr/sbin/nginx -- -g "daemon off;"
```

Stop it with a 30-second grace period before SIGKILL:

```
slinit-start-stop-daemon --stop --retry TERM/30/KILL/5 \\
    --pidfile /run/nginx.pid
```

Check whether it's running:

```
slinit-start-stop-daemon --status --pidfile /run/nginx.pid
```

# SEE ALSO

**slinit**(8), **slinit-service**(5), **slinit-nuke**(8),
**start-stop-daemon**(8) (Debian).
