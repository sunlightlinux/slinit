% SLINIT-LOGOUTHOOKD(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-07-13

# NAME

slinit-logouthookd - clean utmp records when user shells exit

# SYNOPSIS

**slinit-logouthookd** [**\--socket** *PATH*] [**\--perms** *OCTAL*]
                        [**\--version**]

# DESCRIPTION

**slinit-logouthookd** is the s6-linux-init-logouthookd analogue for
slinit. It listens on a Unix domain socket and, for each connected
client, tracks a utmp record that must be cleaned up when the client
dies — typically the user's shell exiting after **login**(1).

The intended integration pattern mirrors s6's:

1. **login**(1) / getty is patched to call a helper before exec'ing
   the user shell. The helper opens a connection to this daemon,
   sends a single line **id line\\n** (utmp inittab-id + line/tty),
   then keeps the fd around across exec.
2. login runs the shell; the connection stays open for the shell's
   lifetime because the shell inherits the fd.
3. When the shell dies, the fd is closed and this daemon's per-
   connection goroutine wakes on EOF and calls **utmp.ClearEntry**
   so **who**(1) / **w**(1) correctly report the user as logged out.

Without a patched login/getty this binary sits idle — no shell ever
connects. slinit ships it so distros that DO want the clean-utmp
behaviour (Sunlight Linux may adopt it) can wire their login stack
against it without inventing a new IPC channel.

**Root-only by design.** An arbitrary user asking to clear the wrong
utmp record is a footgun. Enforced via **SO_PEERCRED** on every
accepted connection; non-root peers are refused with a diagnostic
before any protocol byte is read.

# OPTIONS

**\--socket** *PATH*
:   Path to the Unix domain socket to listen on. Default:
    */run/slinit-logouthookd.sock*.

**\--perms** *OCTAL*
:   Socket file mode. Default: *0600* (root-writable only). Since
    the peer must be uid 0, opening the socket to other users
    accomplishes nothing except surface-area growth.

**\--version**
:   Print the slinit version and exit.

**-h**, **\--help**
:   Print a usage summary and exit.

# PROTOCOL

Each connection sends exactly one line before going quiet:

    ID LINE\n

- **ID** — utmp *ut_id* value (up to 4 chars).
- **LINE** — utmp *ut_line* value (tty name, e.g. **tty1**,
  **pts/0**).

The daemon acknowledges implicitly by staying connected. When the
client closes the fd (typically because the shell it belongs to
died), the daemon writes a *DEAD_PROCESS* record for the (*ID*,
*LINE*) pair to */var/run/utmp* and */var/log/wtmp*.

# EXIT STATUS

**0**
:   Received *SIGTERM* / *SIGINT* and shut down cleanly.

**1**
:   Cannot bind the socket (already in use, permission denied,
    parent directory missing) or non-root at start.

# SEE ALSO

**slinit**(8), **utmp**(5), **login**(1),
**s6-linux-init-logouthookd**(8) — the s6 counterpart this is
modelled after.
