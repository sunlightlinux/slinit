% SLINIT-GETTY(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-09-11

# NAME

slinit-getty - minimal built-in login-prompt binary

# SYNOPSIS

**slinit-getty** [**-p**] *TTY* [*BAUD* [*TERM*]]

# DESCRIPTION

**slinit-getty** opens a terminal, prints */etc/issue*, reads a
username, and execs **login**(1). finit-parity: a built-in
alternative to **agetty**(8) / **mingetty**(8) that reduces the
util-linux dependency on embedded slinit installs while still
supporting the classic getty argument shape (any existing
`command = /sbin/agetty ttyS0 115200` service file can switch to
`command = /sbin/slinit-getty ttyS0 115200` byte-for-byte).

## Flow

1. **setsid**(2) — leave the calling session so the tty becomes
   an unclaimed controlling terminal.
2. Open *TTY* as *O_RDWR|O_NOCTTY* and **dup2**(2) it as fd
   0/1/2.
3. **ioctl**(2) *TIOCSCTTY* to claim it as the process's
   controlling terminal (`force=1` to steal from a prior owner
   like slinit's own **setctty**).
4. Configure **termios**(3) for canonical line input with echo,
   *ONLCR* output post-processing, *ICRNL* input CR-to-NL, 8N1
   character format. *BAUD*, when a recognised decimal rate, is
   applied via *TCSETS*; ignored on virtual terminals.
5. Render */etc/issue* with the classic getty backslash escapes
   (see **ESCAPES** below). Missing file is silent.
6. Print `<hostname> login: ` and read a username. Blank entries
   loop back to re-print the prompt (matches finit's
   `goto restart`).
7. **execve**(2) `/bin/login [-p] -- USERNAME` with the current
   environment (plus *TERM* if given as the third positional
   argument).

# OPTIONS

**-p**
:   Pass **-p** to **login**(1) — preserve the caller's
    environment (useful when slinit's operator has curated the
    login-session env-file).

# ARGUMENTS

*TTY*
:   Terminal device path. Absolute (*/dev/tty1*) or bare
    (*ttyS0*) — the */dev/* prefix is auto-prepended when
    missing.

*BAUD*
:   Serial baud rate as a decimal integer. Accepted values:
    0, 50, 75, 110, 134, 150, 200, 300, 600, 1200, 1800, 2400,
    4800, 9600, 19200, 38400, 57600, 115200, 230400, 460800,
    921600, 1500000. Ignored on virtual terminals.

*TERM*
:   Value exported as *TERM* in the login environment. Omitted:
    caller's *TERM* is inherited unchanged.

# ESCAPES

*/etc/issue* substitutions (per **getty**(8)):

* **\\d** — current date, `Mon Jan _2 2006` format
* **\\l** — tty name without /dev/ prefix
* **\\m** — machine (uname *machine*)
* **\\n** — hostname (uname *nodename*)
* **\\o** — NIS domain name (uname *domainname*)
* **\\r** — kernel release (uname *release*)
* **\\s** — system name (uname *sysname* = "Linux")
* **\\t** — current time, `HH:MM:SS`
* **\\u**, **\\U** — logged-in user count (always "0" here;
  slinit-getty does not walk utmp)
* **\\v** — kernel version string (uname *version*)

Unknown escapes (**\\e**, **\\a**, …) are passed through
verbatim so operator-authored ANSI sequences survive.

# LOGIN BINARY FALLBACK

If none of */bin/login*, */sbin/login*, */usr/bin/login* exist
or are executable, slinit-getty logs an error and drops to a
rescue shell — */sbin/sulogin* → */bin/sulogin* → */bin/sh*.
Matches finit's fallback chain (`_PATH_LOGIN → _PATH_SULOGIN →
_PATH_BSHELL`). An operator without a proper login binary still
gets an interactive prompt instead of a boot-time hang.

# EXAMPLES

Serial console on ttyS0 at 115200 baud:

    tty-serial {
        type = process
        command = /sbin/slinit-getty ttyS0 115200 vt100
        restart = yes
        restart-limit-count = 0
    }

Virtual terminal on tty1 (baud ignored on VTs):

    tty-vt1 {
        type = process
        command = /sbin/slinit-getty tty1
        restart = yes
        restart-limit-count = 0
    }

# EXIT STATUS

**0**
:   **execve** of the login binary or rescue shell succeeded
    (this process was replaced — exit status is that of the
    successor).

**non-zero**
:   Terminal open failed, dup2 failed, or both /bin/login and
    every rescue-shell candidate are missing.

# NOTES

- **slinit-getty** does NOT write a *utmp* login record; the
  invoked **login**(1) writes one itself when it authenticates
  a session. Skipping the intermediate record avoids duplicate
  entries with **who**(1) / **last**(1).

- The **-h** / **?** finit flag (usage banner) is exposed via
  Go's stdlib `-h` / `-help` conventions. **-p** is the only
  behaviour flag.

- Login binary path resolution happens each invocation, so a
  post-boot **/bin/login** install works without restarting
  slinit-getty (the respawn supervisor cycles it after every
  login session).

# SEE ALSO

**login**(1), **getty**(8), **agetty**(8), **slinit**(8),
**slinit-service**(5)
