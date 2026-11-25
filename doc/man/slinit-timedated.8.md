% SLINIT-TIMEDATED(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-11-18

# NAME

slinit-timedated - system time and time zone service for slinit

# SYNOPSIS

**slinit-timedated** [**-exit-idle** *duration*]

# DESCRIPTION

**slinit-timedated** implements *org.freedesktop.timedate1* on the
system bus: the interface GNOME Settings' Date & Time panel uses to set
the time zone and the clock and to turn network time on or off, and
that gnome-settings-daemon uses to follow the time zone automatically.
It is systemd-timedated's interface, so a desktop built for systemd
works unchanged.

The interface belongs to another project, so upstream's definition is
the contract, not slinit's current behaviour — see **STABILITY.md**.

It does the same work as **slinit-timedatectl**(1), with the same code:
the time zone is the */etc/localtime* symlink (and */etc/timezone* where
that file exists), the hardware clock's mode is line 3 of
*/etc/adjtime*, and network time is the first time-sync service found
(**chronyd**, **systemd-timesyncd**, **ntpd**, **openntpd**, **sntp**)
in slinit's system service directories, */etc/slinit.d*, */run/slinit.d*,
*/usr/local/lib/slinit.d* and */lib/slinit.d* (since 3.21.2; before,
only */etc/slinit.d* was searched, so a packaged **chronyd** was not
found and *CanNTP* read false). **slinit-timedatectl**
does not go through this service: it changes the files directly, so it
also works without a bus.

Setting the time zone also rewrites an active **TIMEZONE=** line in
*/etc/rc.conf*. slinit's boot scripts point */etc/localtime* at that
value on every boot, so a zone set only through the symlink would be
lost at the next reboot. An *rc.conf* without such a line is left
alone.

It is started by D-Bus activation when a client first calls the name,
and exits after **-exit-idle** without requests (30 seconds by default;
*0* keeps it running). A request waiting for an administrator's password
keeps it alive.

What it changes, and why a start failed, is logged to syslog as well as
stderr, since its stderr is dbus-daemon's and usually goes nowhere; read
it with **slinit-journalctl -t slinit-timedated** (since 3.21.2).

# OPTIONS

**-exit-idle** *duration*
:   Exit after this long without a request. *0* never exits.

**--version**
:   Print the version and exit.

# AUTHORIZATION

Every change is authorized by polkit, with systemd's action ids, so
polkit rules written for systemd-timedated apply as they are:

*org.freedesktop.timedate1.set-time*
:   **SetTime**. Implies the two below.

*org.freedesktop.timedate1.set-timezone*
:   **SetTimezone**, with the requested zone as the detail *zone*.

*org.freedesktop.timedate1.set-local-rtc*
:   **SetLocalRTC**, with the detail *local_rtc*.

*org.freedesktop.timedate1.set-ntp*
:   **SetNTP**, with the detail *enable*.

By default each needs an administrator's password (*auth_admin_keep*),
even from the active session; GNOME Shell's polkit agent asks for it
when the client marks the call interactive. A call that is not
interactive and would need the password fails with
*org.freedesktop.DBus.Error.InteractiveAuthorizationRequired*.

Root is always allowed. When polkitd is not running, nobody else is:
the service changes the whole machine, and failing open there is not
an option.

# D-BUS INTERFACE

Bus name *org.freedesktop.timedate1*, object
*/org/freedesktop/timedate1*.

**SetTime**(*x* usec_utc, *b* relative, *b* interactive)
:   Set the system clock to *usec_utc* microseconds since the epoch, or
    move it by that much when *relative* is true, counted from when an
    authorization prompt has been answered; the hardware clock
    follows. Refused with *org.freedesktop.timedate1.AutomaticTimeSyncEnabled*
    while the time-sync service runs, and with *InvalidArgs* for a time
    before this release (a clock that far back is certainly wrong).

**SetTimezone**(*s* timezone, *b* interactive)
:   Point the system at an installed IANA zone. Setting the current zone
    again does nothing and asks nothing. With a local-time hardware
    clock, the hardware clock is rewritten to the new local time. The
    kernel is told the new offset, after a zero-offset call that seals
    its first-call clock warp, as systemd does at boot: before 3.22.0
    the first change of a boot moved the clock by the offset and left
    the hardware clock on local time.

**SetLocalRTC**(*b* local_rtc, *b* fix_system, *b* interactive)
:   Make the hardware clock keep local time or UTC. With *fix_system* the
    system clock is set from the hardware clock; otherwise the hardware
    clock is rewritten from the system clock.

**SetNTP**(*b* use_ntp, *b* interactive)
:   Enable and start, or disable and stop, the time-sync service.
    *org.freedesktop.timedate1.NoNTPSupport* when there is none.

**ListTimezones**() → *as*
:   The installed time zones. Needs no authorization.

Properties, all read-only: **Timezone** *s*, **LocalRTC** *b*,
**CanNTP** *b*, **NTP** *b*, **NTPSynchronized** *b*, **TimeUSec** *t*,
**RTCTimeUSec** *t*. **Timezone**, **LocalRTC** and **NTP** are announced
with *PropertiesChanged* when they change. **NTPSynchronized** is true
when the kernel's maximum clock error is below 16 seconds, as systemd
decides it.

# FILES

*/usr/share/dbus-1/system.d/org.freedesktop.timedate1.conf*
:   Who may own and call the name.

*/usr/share/dbus-1/system-services/org.freedesktop.timedate1.service*
:   D-Bus activation.

*/usr/share/polkit-1/actions/org.freedesktop.timedate1.policy*
:   The four polkit actions and their defaults.

*/etc/localtime*, */etc/timezone*, */etc/adjtime*, */etc/rc.conf*
:   What a change writes.

# EXIT STATUS

0 after an idle exit; 1 if the system bus cannot be reached or the name
is already owned.

# SEE ALSO

**slinit-timedatectl**(1), **slinit-hostnamectl**(1),
**slinit-logind**(8), **polkit**(8), **hwclock**(8)
