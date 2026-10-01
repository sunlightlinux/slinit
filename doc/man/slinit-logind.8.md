% SLINIT-LOGIND(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-09-24

# NAME

slinit-logind - login, seat and session manager for slinit

# SYNOPSIS

**slinit-logind** [**--debug**]

**slinit-logind** **--user** [**--debug**]

# DESCRIPTION

**slinit-logind** implements *org.freedesktop.login1* on the system
bus: the interface desktop stacks use to find out who is logged in,
which session owns the screen, and whether the machine may be put to
sleep. It exists so a slinit system can run GDM, GNOME, XFCE and the
portals without elogind's daemon.

The interface belongs to another project, so upstream's definition is
the contract, not slinit's current behaviour — see **STABILITY.md**.
Where the two differ, the difference is the bug.

It is an ordinary slinit service (**type = process**), not part of
PID 1. Nothing in slinit's own operation depends on it; only the
desktop stack does.

## What it is not

**slinit-logind** is not a user service manager. It answers
*org.freedesktop.systemd1* well enough for per-user auto-activation to
succeed, but it starts no units. That is deliberate: it also does not
create */run/systemd/system*, the beacon **sd_booted**(3) looks for, so
**gnome-session** takes its standalone path and spawns each required
component itself rather than waiting for a manager that would never
arrive.

It also does not replace **pam_elogind.so**, which is what actually
calls *CreateSession* at login. That module ships with elogind, so the
elogind *package* is still required even though its daemon is not —
until a native PAM module exists.

# OPTIONS

**--user**
:   Session-bus mode. Connects to the caller's *DBUS_SESSION_BUS*,
    registers only the *org.freedesktop.systemd1* compatibility stub,
    and blocks until the session bus goes away. A session bus is
    per-user and has no authority over hardware or power, so login1 and
    the */run/systemd* tree are skipped entirely. This is the mode a
    per-user auto-activation unit starts.

**--debug**
:   Log every D-Bus method dispatch to stderr.

# SESSION ACTIVITY

A session's *Active* property follows the foreground VT, which is how
elogind decides it. The daemon watches
*/sys/class/tty/tty0/active* — the kernel's record of the foreground
VT — and re-evaluates every session when it changes.

This matters more than it sounds. A greeter such as **gnome-shell**
fades its login dialog to fully transparent when it hands over to a
session, and brings it back only when it observes its own session's
*Active* go from false to true. A daemon that reports every session as
permanently active never produces that transition, and the greeter
comes back as a blank screen after logout.

# FILES

*/run/slinit-logind/sessions/*<id>*.json*
:   One record per session; a superset of what the wire exposes.

*/run/slinit-logind/users/*<uid>*.json*, */run/slinit-logind/seats/*,
*/run/slinit-logind/inhibitors/*
:   The rest of the daemon's own state. Private: the layout is not a
    stable interface, and nothing outside slinit-logind should parse
    it.

*/run/systemd/sessions/*, */run/systemd/users/*,
*/run/systemd/seats/*
:   The libelogind / libsystemd compatibility tree. These records *are*
    a stable interface, because other projects read them. Desktop
    stacks probe this tree at startup and refuse to spawn a greeter if
    it is missing, so the daemon creates it on launch the way elogind
    does.

*/run/systemd/inaccessible/*
:   Immutable placeholders for services that request *PrivateDevices*
    or *InaccessibleDirectories*, so a unit migrated from systemd does
    not fail on its bind-mount step.

Note the absence of */run/systemd/system* — see **What it is not**
above.

# SLEEP, LID AND POWER KEYS

Suspending is not a one-line write to */sys/power/state*. A machine that
sleeps without announcing it wakes with an unlocked desktop, because the
screen locker never heard: every locker waits for the
*PrepareForSleep(true)* signal and locks in response.

So every sleep — *Suspend*, *Hibernate*, *HybridSleep*,
*SuspendThenHibernate* and the *Sleep* dispatcher — goes through one
sequence (since 2.5.1):

1. a *block* inhibitor on *sleep* refuses the request, naming the holder
   and its pid in the error so an operator knows what to go and close;
2. *PrepareForSleep(true)* is emitted and the *PreparingForSleep*
   property becomes true;
3. *delay* inhibitors on *sleep* are waited out, capped by
   **InhibitDelayMaxSec**;
4. **slinitctl suspend** hands the request to PID 1, which runs the sleep
   hook, writes the kernel state, and runs the hook again on wake —
   see **slinit**(8);
5. *PrepareForSleep(false)* is emitted, whether the sleep succeeded or
   not, so a locker is never left believing one is still pending.

Step 4 matters beyond tidiness. There used to be three independent
writers of */sys/power/state* — this daemon, its *Sleep* dispatcher, and
**slinitctl suspend** — so which door a request came through decided
whether anything else happened. There is one now.

*HybridSleep* and *SuspendThenHibernate* are accepted and performed as
their nearest single kernel state, hibernate and suspend respectively.
A real hybrid sleep writes an image and then suspends to RAM, and
suspend-then-hibernate needs an RTC alarm to come back and finish; doing
something else under those names is how a laptop loses work, so they are
honest aliases rather than emulations.

## Inhibitors

*Inhibit(what, who, why, mode)* returns a descriptor. The lock lasts
until that descriptor is closed — there is no release method, which is
the point: a client that crashes cannot leave a machine permanently
unsuspendable.

*what* is a colon-separated list of *shutdown*, *sleep*, *idle*,
*handle-power-key*, *handle-suspend-key*, *handle-hibernate-key*,
*handle-lid-switch*, *handle-reboot-key*. An unrecognised class is an
error rather than a lock that silently guards nothing.

*mode* is *block*, which refuses the operation, or *delay*, which asks
for time before it. A locker takes a delay lock, locks the screen when it
sees *PrepareForSleep(true)*, then closes the descriptor to say it is
done — which is why closure is detected per sleep rather than per
session.

Enforcement covers sleep and the key handlers below. *shutdown* and
*idle* locks are recorded and reported but not yet enforced; **loginctl
list-inhibitors** shows them.

# CONFIGURATION

Read at start-up from the first file that exists:

*/etc/slinit/logind.conf*
:   slinit's own.

*/etc/elogind/logind.conf*
:   read when the above is absent, so a machine migrating off elogind
    keeps its settings without the operator copying them.

The format is systemd's *logind.conf*: an INI file whose **[Login]**
section holds the keys. Keys outside that section are ignored, as are
keys inside it that this daemon has no opinion on. A value that does not
parse is reported on stderr and the default kept — the daemon never
refuses to start over one bad line, because that would take the desktop
with it.

**HandleLidSwitch**=, **HandleLidSwitchExternalPower**=,
**HandleLidSwitchDocked**=
:   What to do when the lid shuts. Docked wins over external power: a
    docked laptop with the lid shut is a desktop. An unset
    **HandleLidSwitchExternalPower** follows **HandleLidSwitch**, as in
    systemd, so one key covers a machine that behaves the same on
    battery and mains — but an explicit *ignore* there is a decision and
    is honoured.

**HandlePowerKey**=, **HandleSuspendKey**=, **HandleHibernateKey**=
:   What to do when the corresponding key is pressed. The action runs on
    key-down only.

    Accepted actions: *ignore*, *poweroff*, *reboot*, *halt*, *suspend*,
    *hibernate*, *hybrid-sleep*, *suspend-then-hibernate*, *lock*. An
    unknown action is reported and treated as *ignore* — a typo that
    silently became *poweroff* is the worst available outcome.

**HoldoffTimeoutSec**=
:   How long lid events are suppressed after start-up and after each
    wake. Default 30. This is not politeness: a lid switch that still
    reads *closed* as the machine resumes would suspend it again at once,
    which an operator experiences as a laptop that will not wake up.
    Opening the lid clears the window early.

**InhibitDelayMaxSec**=
:   How long a sleep waits for *delay* inhibitors to clear. Default 5,
    matching systemd, because lockers are written against that number.
    Timing out proceeds with the sleep: a locker that crashed holding a
    lock must not keep a lid-shut laptop awake.

Both timeouts also accept a Go duration, so *1500ms* is not a parse
error.

## Every handler defaults to off

With no configuration file, every **Handle*** key and the lid switch are
*ignore*, which is **not** systemd's default — it powers off on the power
key and suspends on lid close.

The difference is deliberate. Turning those on would change what the
hardware does the moment this daemon gained a watcher, on every existing
installation, with no file edited to ask for it. An operator opts in.

It is also frequently the better answer on a desktop: GNOME's
settings-daemon and XFCE's power manager both read these properties to
decide whether logind already owns a key, and *ignore* tells them to
handle it themselves — which they do, with a UI and a user setting.
Configure these for a bare window manager, or when you want the policy to
hold with no session running.

# D-BUS INTERFACE

Bus name *org.freedesktop.login1*, object */org/freedesktop/login1*,
interface *org.freedesktop.login1.Manager*.

Sessions, users and seats
:   *CreateSession*, *ReleaseSession*, *ActivateSession*,
    *ActivateSessionOnSeat*, *LockSession*, *UnlockSession*,
    *LockSessions*, *UnlockSessions*, *KillSession*, *KillUser*,
    *TerminateSession*, *TerminateUser*, *TerminateSeat*,
    *SetUserLinger*, *GetSession*, *GetSessionByPID*, *GetUser*,
    *GetUserByPID*, *GetSeat*, *ListSessions*, *ListUsers*,
    *ListSeats*.

Power and sleep
:   *PowerOff*, *Reboot*, *Halt*, *Suspend*, *Hibernate*,
    *HybridSleep*, *SuspendThenHibernate*, each with a
    *...WithFlags* variant, and the matching *Can...* queries that
    desktops use to decide whether to grey out a menu entry.

Inhibitors
:   *Inhibit*, *ListInhibitors*.

The daemon answers *Introspect* on the manager path. That is not
optional politeness: the XFCE session, gvfs and the portals probe it
before calling anything, and treat a missing response as the daemon
being absent, which drops them into degraded modes with broken menus
and no power controls.

# EXIT STATUS

Exits non-zero if the state directories cannot be created, the system
bus cannot be reached, the objects cannot be exported, or the bus name
cannot be claimed. The last of these usually means elogind's daemon is
still running and holding *org.freedesktop.login1* with
*AllowReplacement=false*; stop it first.

# SEE ALSO

**slinit**(8), **slinitctl**(8), **slinit-service**(5)

*org.freedesktop.login1* is defined by systemd; **logind**(8) and
**org.freedesktop.login1**(5) from that project are the reference.
