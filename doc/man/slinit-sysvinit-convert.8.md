% SLINIT-SYSVINIT-CONVERT(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-10-02

# NAME

slinit-sysvinit-convert - port /etc/inittab to slinit service files

# SYNOPSIS

**slinit-sysvinit-convert** [*flags*] *inittab*

# DESCRIPTION

**slinit-sysvinit-convert** reads an */etc/inittab* — sysvinit's or
busybox's — and emits equivalent slinit service files, completing the
converter set beside
**slinit-openrc-convert**(8), **slinit-runit-convert**(8) and
**slinit-systemd-convert**(8).

inittab is one file holding many entries rather than one directory per
service, so batch output is the normal mode: every entry that carries a
process becomes its own service file, and every entry that does not is
reported on standard error together with what slinit does instead.

That split is the point. Roughly half of inittab's fifteen actions are
not services at all — they are settings, and slinit expresses them
elsewhere. Converting them into service files would produce something
that looks right and does nothing.

## Actions that become services

| inittab | slinit |
|---|---|
| **respawn** | **type**=*process*, **restart**=*yes* |
| **wait**, **once**, **boot**, **bootwait** | **type**=*scripted* |
| **sysinit** | **type**=*scripted*, with no dependencies |
| **off** | **manual**=*yes* — kept for reference, never started |
| **ondemand** | **manual**=*yes* — started by whatever should trigger it |

The *id* column becomes **inittab-id**, and for a recognised getty the
tty becomes **inittab-line**. Those are slinit's utmp bookkeeping
directives, which is exactly what sysvinit does with that column, so
**who**(1) and **last**(1) keep working across the migration. Service
file names are lowercased; the directive values keep their original
case, so a *ttyS0* getty lands in *getty-ttys0* with
**inittab-line**=*ttyS0*.

## Actions that are settings, and what replaces them

**initdefault**
:   Names the runlevel to enter at boot. slinit boots whatever its boot
    service waits for; the report names the *runlevel-N* target to point
    that at.

**ctrlaltdel**
:   Dropped. slinit handles Ctrl+Alt+Del itself and reboots — there is
    nothing to configure, and an entry here would be a second, conflicting
    answer.

**powerwait**, **powerfail**, **powerokwait**, **powerfailnow**
:   slinit dispatches *SIGPWR* to a single hook,
    */etc/slinit/power-hook*, with *failing*, *ok* or *low* as its
    argument — so four inittab entries collapse into four cases of one
    **case** statement. The report says which argument each entry maps
    to. See **slinit**(8) POWER EVENTS.

**kbrequest**
:   No slinit equivalent; it needs the kernel's *KDSIGACCEPT*. Reported
    as a warning and not converted.

## Runlevels

slinit models a runlevel as a service whose **waits-for** list is its
members, as **rc-update**(8) does. A service file cannot express its own
membership, so the converter prints the commands instead:

    slinitctl --from runlevel-3 enable getty-tty1

Runlevels **0** and **6** are halt and reboot. A service "in" them runs
while the system goes down, which slinit expresses with
**stop-command** rather than membership, so they are not wired.

## Dialects

Both inittab dialects are supported, each read out of its own source:
sysvinit's fifteen actions from its **inittab**(5) and *init.c*,
busybox's eight from the action table in *init/init.c*.

Five action names are shared — **sysinit**, **wait**, **once**,
**respawn**, **ctrlaltdel** — and mean the same thing in both. The other
thirteen belong to exactly one dialect, so the action column is never
ambiguous.

What *is* ambiguous is the **first column**, and it is the only thing
**\--dialect** actually decides:

| dialect | first column | becomes |
|---|---|---|
| sysvinit | a short utmp entry id | **inittab-id** |
| busybox | the tty to run the command on | **tty-path** |

busybox's parser also never reads the **runlevel** column at all, so in
that dialect no runlevel wiring is emitted and the ignored column is
reported. And a command with a leading **-** asks busybox for a
controlling terminal; **tty-path** already arranges exactly that
(*Setsid*+*Setctty*), so the dash is removed from the command — left in,
it would be exec'd as part of the path — and the removal is reported.

With **\--dialect**=*auto*, the choice is inferred and always printed. An
action unique to one dialect settles it; failing that, busybox's shape is
an empty runlevel column plus a tty in the first one. A file with neither
signal is read as sysvinit, because applying its reading of the first
column by mistake is the harmless direction.

busybox's three own actions are not services:

**askfirst**
:   Respawn, but wait for Enter first so a console stays quiet until
    someone wants it. slinit has no equivalent, so it is written as a
    plain **respawn** — which *will* start immediately — and reported, so
    the lost prompt is not a surprise. Add **manual**=*yes* if that
    console should stay idle.

**shutdown**
:   Run and waited for while the system goes down. slinit's equivalent is
    */etc/slinit/shutdown-hook*, which already receives the shutdown type
    as *$1*; per service, **stop-command**.

**restart**
:   What busybox exec()s to replace itself on *SIGQUIT*, and only the
    first such entry was ever used. slinit's equivalent is
    **slinitctl soft-reboot**, which re-execs slinit and keeps the
    service graph.

An action neither dialect defines is **refused by name and line number**
rather than guessed at: a wrong guess would emit a service that silently
does the wrong thing, which mid-migration is the worst available
outcome.

# FLAGS

**\--dialect**=*sysvinit*|*busybox*|*auto*
:   Which inittab grammar the input is written in. Default *auto*, which
    infers it from the file and prints what it chose. See **Dialects**.

**\--output-dir**=*DIR*
:   Write one file per service into *DIR*. Without it the files go to
    standard output, separated by a header comment naming each one.

**\--dry-run**
:   Report what would be written, with sizes, and touch nothing.

**\--verbose**
:   Name each file as it is written.

# EXAMPLES

Review before committing to anything:

    slinit-sysvinit-convert --dry-run /etc/inittab

Convert, keeping the report for later:

    slinit-sysvinit-convert --output-dir=/etc/slinit.d /etc/inittab 2> inittab-notes.txt

Read the generated files without writing them:

    slinit-sysvinit-convert /etc/inittab 2>/dev/null | less

# EXIT STATUS

**0**
:   Every line parsed. Entries may still have been reported as warnings;
    read standard error.

**1**
:   At least one line was not an inittab entry, or a file could not be
    written. The lines that did parse are still converted.

**2**
:   Wrong number of arguments.

# FILES

*/etc/inittab*
:   The input. Nothing is written to it.

*/etc/slinit/power-hook*
:   Where the **powerfail** family goes. See **slinit**(8).

# SEE ALSO

**slinit**(8), **slinit-service**(5), **slinitctl**(8),
**slinit-openrc-convert**(8), **slinit-runit-convert**(8),
**slinit-systemd-convert**(8), **rc-update**(8), **inittab**(5),
**busybox**(1)
