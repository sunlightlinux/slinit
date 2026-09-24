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
