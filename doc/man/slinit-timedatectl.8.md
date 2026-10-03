% SLINIT-TIMEDATECTL(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-10-03

# NAME

slinit-timedatectl - query or change the system clock, timezone and RTC, without D-Bus

# SYNOPSIS

**slinit-timedatectl** [*OPTIONS*...] *COMMAND* [*ARGUMENT*]

# DESCRIPTION

**slinit-timedatectl** matches systemd's **timedatectl**(1) command
surface and reaches the same settings directly on disk, with no D-Bus and
no **systemd-timedated** running — the same shape as
**slinit-hostnamectl**(8).

* */etc/localtime* — the timezone, as a symlink into
  */usr/share/zoneinfo*
* */etc/timezone* — kept in step when the system has one
* */etc/adjtime* — whether the RTC is held in local time or UTC
* */dev/rtc* — read with *RTC_RD_TIME*

With no *COMMAND*, **status** is implied.

# COMMANDS

**status**
:   Show the local and universal time, the RTC, the timezone and whether
    a time-sync service is active.

**show**
:   The same fields as *KEY*=*VALUE*, for scripts.

**set-time** *TIME*
:   Set the wall clock. Accepts RFC 3339, systemd's
    "*YYYY-MM-DD hh:mm:ss*" form, and *@*\ *epoch*.

**set-timezone** *ZONE*
:   Replace the */etc/localtime* symlink, atomically, with the named zone
    — *Europe/Bucharest* and so on.

**list-timezones**
:   Every zone found under */usr/share/zoneinfo*.

**set-local-rtc** *BOOL*
:   Record whether the RTC runs in local time (true) or UTC (false), by
    writing the third line of */etc/adjtime*. UTC is the right answer on
    anything that is not dual-booting Windows.

**set-ntp** *BOOL*
:   Enable or disable network time synchronisation — which here means
    enabling or disabling the time-sync *service*, since slinit has no
    built-in NTP client. It looks for a known one under
    */etc/slinit.d/*: **chronyd**, **systemd-timesyncd**, **ntpd**,
    **openntpd** or **sntp**, and fails with that list named if none of
    them is there, rather than silently reporting success.

# OPTIONS

**\--adjust-system-clock**
:   With **set-local-rtc**, also move the system clock so the wall time
    stays where it was.

**\--json**=*off*|*pretty*|*short*, **-j**
:   Machine-readable output. **-j** is *pretty* on a terminal and *short*
    otherwise.

**\--no-pager**
:   Do not pipe output through a pager. Relevant to
    **list-timezones**.

**\--no-ask-password**
:   Accepted and does nothing; there is no prompt to suppress.

**-h**, **\--help**, **\--version**
:   Help, and the slinit version.

## Parsed but rejected

These are accepted on the command line so a line copied from a systemd
system fails with a clear message rather than an unknown-flag error, and
then refused because the thing behind them does not exist here:

**-H**, **\--host**=[*USER*@]*HOST*, **-M**, **\--machine**=*CONTAINER*
:   D-Bus over SSH, and into an **nspawn** container.

**timesync-status**, **show-timesync**, **ntp-servers**, **revert**
:   All four are **systemd-timesyncd** introspection or runtime
    configuration. slinit does not implement an NTP client, so there is no
    daemon to introspect; ask the time-sync service you actually run.

# EXIT STATUS

**0** on success. Non-zero if a file could not be read or written, the
timezone was unknown, no time-sync service was found for **set-ntp**, or
the command is one of the unsupported four.

# EXAMPLES

    slinit-timedatectl
    slinit-timedatectl show
    slinit-timedatectl set-timezone Europe/Bucharest
    slinit-timedatectl set-time '2026-10-03 09:30:00'
    slinit-timedatectl set-time @1790000000
    slinit-timedatectl set-local-rtc false --adjust-system-clock
    slinit-timedatectl set-ntp true

# FILES

*/etc/localtime*
:   Symlink to the active zone under */usr/share/zoneinfo*.

*/etc/timezone*
:   The zone name, written only where the system already keeps this file.

*/etc/adjtime*
:   Third line is *UTC* or *LOCAL*.

# SEE ALSO

**slinit**(8), **slinit-hostnamectl**(8), **slinitctl**(8),
**timedatectl**(1), **adjtime_config**(5), **hwclock**(8)
