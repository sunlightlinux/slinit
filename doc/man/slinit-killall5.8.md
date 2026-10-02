% SLINIT-KILLALL5(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-10-02

# NAME

slinit-killall5 - signal every process except init, the caller and its session

# SYNOPSIS

**slinit-killall5** **-**\ *signum* [**-o** *omitpid*[,*omitpid*...]] ...

# DESCRIPTION

**slinit-killall5** is a drop-in replacement for sysvinit's
**killall5**(8). init.d scripts call it on their shutdown paths — usually
`killall5 -15` and then `killall5 -9`, or `killall5 -15 -o $$` when the
script needs to survive its own sweep — so a distribution can point
*/sbin/killall5* at this and leave the scripts untouched.

The following are never signalled:

* **PID 1**
* this process
* every process in **this process's session** — the shell that invoked
  it, and anything else that shell started
* every **kernel thread**, recognised the way sysvinit recognises one:
  *startcode* and *endcode* in */proc/*\ *pid*\ */stat* are both zero,
  because a kernel thread has no user address space
* every **zombie** — it cannot be killed, and counting it would make the
  exit status claim a kill that did not happen
* anything named by **-o**

With **no** **-o** list the whole system is frozen with
*kill(-1, SIGSTOP)* before */proc* is read and resumed with *SIGCONT*
afterwards, so the process list cannot change midway. With an **-o** list
it is not frozen — upstream does not, and the reason shows in the shape
of the problem: a caller passing **-o** is usually a script that has to
keep running.

## Relationship to slinit-nuke

**slinit-nuke**(8) is the neighbouring tool and deliberately not this
one. It takes no signal argument and has no omit list, and it applies a
policy — SIGTERM, a grace period, then SIGKILL — aimed at an operator
rescuing a machine whose init has hung. **slinit-killall5** is a
primitive that does exactly what it is told, once, which is what a script
on a shutdown path needs.

## Deliberate differences from sysvinit

sysvinit's **killall5** is also installed as **pidof**(8), the same
binary under another name. That half is not reproduced here: **pidof**
ships in *procps* on every distribution slinit targets, and a second
implementation would be a second thing to keep correct for no gain.

sysvinit calls *mlockall(MCL_CURRENT|MCL_FUTURE)*. This locks only
**MCL_CURRENT**: by the time init.d scripts reach killall5 the
filesystems may be read-only with no swap, so staying resident matters —
but **MCL_FUTURE** in a Go program would lock every later allocation the
runtime makes, which is a liability rather than insurance for a process
whose remaining work is one walk of */proc*.

It also calls *signal(SIGSTOP, SIG_IGN)* and *signal(SIGKILL, SIG_IGN)*,
which cannot work — neither signal can be caught or ignored — and does
not need to: Linux excludes the calling process from *kill(-1, sig)*.
Only *SIGTERM* is claimed here, and only so that a signal aimed at this
process by something else cannot cut a sweep short.

# OPTIONS

**-**\ *signum*
:   The signal to send, as a number — **-15**, **-9**. Required, and
    exactly one. Signal *names* are not accepted, because sysvinit does
    not accept them and the point of this program is that an existing
    script does not have to change.

**-o** *omitpid*[,*omitpid*...]
:   Do not signal these pids. May be repeated, and each may be a
    comma-separated list. **-o**\ *pid* with no space works too, as it
    does upstream.

# EXIT STATUS

**0**
:   At least one process was signalled.

**2**
:   Nothing was signalled. Everything alive was init, this process, this
    session, a kernel thread, a zombie or omitted.

**1**
:   */proc* could not be read, or the arguments were not understood. A
    script can tell this apart from **2**: one means "nothing to do", the
    other means "could not look".

# EXAMPLES

The classic shutdown pair:

    slinit-killall5 -15
    sleep 2
    slinit-killall5 -9

Sweep everything but keep the calling script alive — redundant, since its
session is spared anyway, but harmless and what existing scripts write:

    slinit-killall5 -15 -o $$

Spare a database that is mid-checkpoint along with its helper:

    slinit-killall5 -15 -o 4821,4822

# SEE ALSO

**slinit-nuke**(8), **slinit**(8), **slinitctl**(8), **killall5**(8),
**pidof**(8)
