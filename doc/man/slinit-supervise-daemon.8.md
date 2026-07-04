# NAME

slinit-supervise-daemon - start a non-forking daemon and restart it if
it crashes (OpenRC-compatible)

# SYNOPSIS

**slinit-supervise-daemon** *SVCNAME* [*OPTIONS*] {**--start** |
**--stop** | **--signal** *SIG*} [**--** *ARGS*...]

# DESCRIPTION

**slinit-supervise-daemon** is a drop-in replacement for OpenRC's
**supervise-daemon**(8): it starts a daemon that MUST NOT fork, keeps
it alive across crashes via a configurable respawn policy, and
forwards **--signal** / **--stop** to the running daemon.

The tool is standalone. It performs no coordination with the running
**slinit** daemon and adds no service description; it exists so ported
**/etc/init.d** scripts (which often hard-code
`supervise-daemon svcname --start --exec …`) keep working under
**slinit**. For production services managed by slinit natively, define
a real service description under **/etc/slinit.d** instead — that
provides supervision, dependencies, and hardening as first-class
citizens.

The first positional argument is the service name; it is recorded in
diagnostic output and used to disambiguate multiple supervisors sharing
a directory. Every argument after **--** is passed straight to the
daemon.

# MODES

**-S**, **--start**  *(default)*
:   Fork off a detached supervisor that owns the daemon's lifecycle,
    then wait until the supervisor writes **--pidfile** before
    returning. The daemon PID is recorded alongside the supervisor
    pidfile as *PIDFILE***.daemon**.

**-K**, **--stop**
:   Read the supervisor pidfile, deliver **SIGTERM**, and wait for
    the supervisor to exit. The supervisor's own shutdown path kills
    the daemon and cleans both pidfiles.

**-s**, **--signal** *SIG*
:   Deliver *SIG* directly to the supervised daemon (via
    *PIDFILE***.daemon**). Bypasses the supervisor so a **SIGHUP**
    reload does not race with the respawn logic. Caveat: sending
    **SIGTERM** or **SIGKILL** this way will be treated as a crash by
    the supervisor and trigger a respawn — use **--stop** for
    permanent shutdown.

# PROCESS ATTRIBUTES

**-x**, **--exec** *PATH*
:   Binary to run under supervision. Required for **--start**.

**-p**, **--pidfile** *PATH*
:   Path to the supervisor pidfile. Required for **--start**,
    **--stop**, and **--signal**. The daemon PID is written to
    *PATH***.daemon**.

**-u**, **--user** *USER*[**:***GROUP*]
:   Drop credentials before **exec**(2). The optional group shorthand
    replaces the value **--group** would supply.

**-g**, **--group** *GROUP*
:   Daemon primary group.

**-d**, **--chdir** *DIR*
:   **chdir**(2) before exec.

**-r**, **--chroot** *DIR*
:   **chroot**(2) before exec.

**-N**, **--nicelevel** *N*
:   **setpriority**(2) applied post-fork.

**--oom-score-adj** *N*
:   Write to /proc/*PID*/oom_score_adj. Range **-1000** to **1000**.

**-k**, **--umask** *OCT*
:   Octal umask.

**-I**, **--ionice** *CLASS*[**:***LEVEL*]
:   **ioprio_set**(2). *CLASS* is **rt**/**realtime** (1),
    **be**/**best-effort** (2), or **idle** (3). *LEVEL* 0-7.

**-e**, **--env** *KEY*=*VAL*
:   Append an environment entry. Repeatable.

**-0**, **--stdin** *FILE*
:   Redirect the daemon's stdin from *FILE*.

**-1**, **--stdout** *FILE*
:   Redirect stdout to *FILE* (append).

**-2**, **--stderr** *FILE*
:   Redirect stderr to *FILE* (append).

**--stdout-logger** *CMD*
:   Pipe stdout to *CMD*'s stdin. Whitespace-split, no shell
    interpolation.

**--stderr-logger** *CMD*
:   Same for stderr.

# RESPAWN POLICY

**-D**, **--respawn-delay** *DURATION*
:   Fixed delay before restarting a crashed daemon. Default **0**.

**-P**, **--respawn-period** *DURATION*
:   Rolling window used to count crashes. Default **12sec**.

**-m**, **--respawn-max** *N*
:   Maximum number of respawns allowed within one period; the
    supervisor gives up when exceeded. **0** = unlimited. Default
    **10**.

**--respawn-delay-step** *DURATION*
:   Additional backoff added per respawn (linear step). Default
    **128ms**.

**--respawn-delay-cap** *DURATION*
:   Ceiling for the stepped backoff. Default **30sec**.

Durations accept OpenRC forms (**ms**, **sec**, **min**, **hour**) or
Go's **time.ParseDuration** style (**500ms**, **2h**).

# STOP ESCALATION

**-R**, **--retry** *SPEC*
:   Retry schedule applied when the supervisor's shutdown path signals
    the daemon. Two forms:

    - Integer seconds: **--retry 5** is shorthand for
      **--retry TERM/5/KILL/5**.
    - Slash-separated: **--retry TERM/30/KILL/5**.

    Default: **TERM/5/KILL/5**.

# HARDENING (RUNNER-WRAPPED)

Same shape as **slinit-start-stop-daemon**(8): the following are
applied inside the child, via **slinit-runner**(8), before **exec**(2).

**--capabilities** *LIST*
:   Ambient+bounding capability set.

**--secbits** *BITS*
:   **PR_SET_SECUREBITS** bitmask.

**--no-new-privs**
:   Set **PR_SET_NO_NEW_PRIVS**(2).

# ACCEPTED, NOT IMPLEMENTED

These flags parse successfully so ported init.d scripts do not crash,
but the behaviour is either owned by the calling script or intentionally
skipped:

- **-a**, **--healthcheck-timer** *DURATION* — OpenRC would call a
  shell **healthcheck()** function. slinit does not own that hook.
- **-A**, **--healthcheck-delay** *DURATION* — as above.
- **--notify readiness=...** — supervise-daemon is the reader in
  OpenRC's model, not the writer; the supervisor is considered ready
  as soon as the daemon exec's.

# COMMON OPTIONS

**-v**, **--verbose**
:   Extra diagnostics on stderr.

**-h**, **--help**
:   This help.

**-V**, **--version**
:   Version string.

# EXIT STATUS

- **0**: success
- **1**: already running (**--start**) or not running
- **2**: syntax / bad usage
- **3**: unsupported feature
- **4**: insufficient privileges or spawn failure
- **5**: stale pidfile (supervisor gone; **--stop**/**--signal**)

# EXAMPLES

Start nginx under a supervisor with 3-per-minute respawn budget:

```
slinit-supervise-daemon nginx --start \\
    --pidfile /run/nginx.supervisor.pid \\
    --exec /usr/sbin/nginx \\
    --respawn-max 3 --respawn-period 60 \\
    -- -g "daemon off;"
```

Reload nginx (**SIGHUP** to the daemon, not the supervisor):

```
slinit-supervise-daemon nginx --signal HUP \\
    --pidfile /run/nginx.supervisor.pid
```

Stop the supervisor (and the nginx it manages):

```
slinit-supervise-daemon nginx --stop \\
    --pidfile /run/nginx.supervisor.pid \\
    --retry TERM/10/KILL/5
```

# SEE ALSO

**slinit**(8), **slinit-service**(5), **slinit-start-stop-daemon**(8),
**supervise-daemon**(8) (OpenRC).
