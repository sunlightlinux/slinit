% TIMEDATECTL(1) slinit | Sunlight Linux
% Ionut Nechita
% 2026-08-09

# NAME

timedatectl - control the system time and date (slinit-native, D-Bus-free)

# SYNOPSIS

**timedatectl** [*OPTIONS*] **status**\
**timedatectl** [*OPTIONS*] **show**\
**timedatectl** [*OPTIONS*] **set-time** *TIME*\
**timedatectl** [*OPTIONS*] **set-timezone** *ZONE*\
**timedatectl** [*OPTIONS*] **list-timezones**\
**timedatectl** [*OPTIONS*] **set-local-rtc** *BOOL* [**\--adjust-system-clock**]\
**timedatectl** [*OPTIONS*] **set-ntp** *BOOL*

The installed binary is **slinit-timedatectl**; a **timedatectl**
symlink is provided for systemd muscle memory.

# DESCRIPTION

**timedatectl** may be used to query and change the system clock and
its settings, matching the systemd **timedatectl**(1) CLI surface for
local operations. Under slinit there is no D-Bus and no **timedated**:
the utility reads and writes the on-disk sources of truth directly.

Sources of truth:

- **/etc/localtime** — symlink into */usr/share/zoneinfo* naming the
  current IANA timezone (read via **readlink**(2))
- **/etc/timezone** — Debian-family text file with the zone name;
  only touched when it already exists
- **/etc/adjtime** — line 3 records whether the RTC keeps UTC or
  local time (**UTC** or **LOCAL**); missing file defaults to UTC
- **/dev/rtc** — hardware clock, read via the **RTC_RD_TIME** ioctl
- **/etc/slinit.d/** — probed for the presence of a time-sync
  service (chronyd, systemd-timesyncd, ntpd, openntpd, sntp)

Setting the system time uses **clock_settime**(2) on
**CLOCK_REALTIME** and requires **CAP_SYS_TIME**. Setting the
timezone swaps */etc/localtime* via a tmp-symlink + rename dance so
concurrent readers never observe an inconsistent state.

# COMMANDS

**status**
:   Show current time / timezone / RTC / NTP settings. This is the
    default when no command is given. Two-column text table; empty
    fields are omitted. With **\--json** output is machine-readable.

**show**
:   Emit the same fields as **KEY=VALUE** lines with timestamps in
    microseconds since the Unix epoch, matching the systemd
    D-Bus-property dump format.

**set-time** *TIME*
:   Set the system clock. Accepted forms:

    - **now** — the current wall-clock instant (no-op sanity test)
    - **@**\ *EPOCH* — decimal seconds since 1970 (fraction ok)
    - RFC 3339, e.g. *2024-01-15T14:30:00Z* or with an offset
    - systemd form: *YYYY-MM-DD HH:MM[:SS]* in local time
    - *HH:MM[:SS]* — today's date at the given time
    - Relative: *+5min*, *-2h*, *+1d*, *+30s* (unit suffixes match
      **systemd-analyze**(1) timespan)

**set-timezone** *ZONE*
:   Validate *ZONE* against */usr/share/zoneinfo* and update
    */etc/localtime* atomically. If */etc/timezone* is already
    present, it is refreshed too. Rejects paths containing `..` or
    starting with `/`.

**list-timezones**
:   Print every known zone, one per line, sorted. Prefers
    *zone1970.tab*, falls back to *zone.tab*, and finally to walking
    the zoneinfo tree for TZif-magic files.

**set-local-rtc** *BOOL* [**\--adjust-system-clock**]
:   Record whether the RTC keeps local time (*BOOL* = true / yes /
    on / 1) or UTC (false / no / off / 0) by writing */etc/adjtime*
    line 3. With **\--adjust-system-clock**, also shell out to
    **hwclock**(8) to re-write the hardware clock under the new
    interpretation; when hwclock is not on **PATH** a clear error is
    returned rather than silently succeeding.

**set-ntp** *BOOL*
:   Enable or disable the local time-sync service. slinit-timedatectl
    probes */etc/slinit.d/* for the first of *chronyd*,
    *systemd-timesyncd*, *ntpd*, *openntpd*, *sntp*, then calls
    **slinitctl enable / disable** and **start / stop** as
    appropriate. Absence of any known service yields a clear error.

# OPTIONS

**-h**, **\--help**
:   Show usage and exit.

**\--version**
:   Print the slinit version and exit.

**\--no-pager**
:   Do not pipe output through a pager. Accepted; slinit-timedatectl
    does not run a pager anyway.

**\--no-ask-password**
:   Accepted for compatibility with the systemd CLI; slinit-timedatectl
    never prompts interactively.

**\--adjust-system-clock**
:   Only meaningful with **set-local-rtc**; see above.

**\--json**=*MODE*
:   Emit output as JSON. *MODE* is *off* (text; the default),
    *pretty* (indented), or *short* (single line).

**-j**
:   Shorthand for **\--json**=*pretty* on a TTY, or **\--json**=*short*
    otherwise.

**-p**, **\--property**=*NAME*
:   Accepted for compatibility. Not currently used to filter output.

**-a**, **\--all**, **\--value**, **\--monitor**
:   Accepted for compatibility. No runtime effect.

# UNSUPPORTED

The following systemd features are accepted at the CLI level (so
scripts do not fail argument parsing) but return an error at runtime:

**-H**, **\--host**=[*USER*@]*HOST*
:   D-Bus over SSH.

**-M**, **\--machine**=*CONTAINER*
:   nspawn container.

**timesync-status**, **show-timesync**, **ntp-servers**, **revert**
:   All specific to systemd-timesyncd, which slinit does not integrate.

# EXIT STATUS

**0**
:   Success.

**1**
:   Runtime error (I/O failure, invalid value, unsupported option).

**2**
:   Command-line usage error.

# FILES

*/etc/localtime*, */etc/timezone*, */etc/adjtime*, */dev/rtc*
:   See DESCRIPTION.

*/usr/share/zoneinfo/zone1970.tab*, */zone.tab*
:   Preferred sources for **list-timezones**.

# EXAMPLES

Show the current time and settings:

    timedatectl

Switch to Bucharest local time:

    timedatectl set-timezone Europe/Bucharest

Set the wall clock to a specific instant:

    timedatectl set-time '2026-08-09 14:30:00'
    timedatectl set-time '+5min'

Tell the kernel that the RTC keeps UTC (recommended):

    timedatectl set-local-rtc no

Enable NTP-based time synchronization:

    timedatectl set-ntp yes

Machine-readable output:

    timedatectl --json=short status
    timedatectl show

# SEE ALSO

**clock_settime**(2), **hwclock**(8), **tzset**(3), **tzfile**(5),
**adjtime_config**(5), **systemd-analyze**(1)

# NOTES

slinit-timedatectl is a byte-for-byte independent implementation of
the systemd **timedatectl**(1) CLI, written in Go, with zero D-Bus
dependency. It shares neither code nor state with **timedated**.
