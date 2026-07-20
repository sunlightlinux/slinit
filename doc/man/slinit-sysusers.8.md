% SLINIT-SYSUSERS(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-07-18

# NAME

slinit-sysusers - declarative user and group creation at boot

# SYNOPSIS

**slinit-sysusers** [**\--dirs** *DIRS*] [**\--dry-run**]

# DESCRIPTION

**slinit-sysusers** applies **systemd-sysusers.d**(5) directives at
boot to create system users, groups, and group memberships. Reads
config from */usr/lib/sysusers.d/\*.conf*, */etc/sysusers.d/\*.conf*,
and */run/sysusers.d/\*.conf* by default (later directories override
earlier ones by basename — same overlay semantics as
**tmpfiles.d**(5) and systemd's own sysusers).

The tool shells out to **useradd**(8), **groupadd**(8), and
**usermod**(8) for the actual account manipulation, so it inherits
whatever password-database backend the host uses (files, LDAP,
sssd, etc.) via the standard nss stack. Idempotent — pre-existing
entries with matching UID/GID are left alone; conflicts are
reported.

Typical use is boot-time bootstrap: a package's *.conf* declares the
service account it needs, and the account is present the next time
that service starts. Removes the manual "chown -R after install"
step.

# CONFIG FORMAT

Each line in a *.conf* file is one directive:

    TYPE   NAME    ID   GECOS           HOME         SHELL

Directives:

**u** *name* *uid*[:*gid*] *"gecos"* *home* *shell*
:   Create a user. *uid* may be a specific number, **-** for
    "kernel picks", or **uid:gid** to bind both.

**g** *name* *gid*
:   Create a group. *gid* may be a specific number or **-**.

**m** *user* *group*
:   Add *user* to supplementary group *group*.

**r** — *lo*-*hi*
:   Reserve a UID/GID range for kernel/system use. Advisory;
    slinit-sysusers just records the range.

Comments start with **#**. Fields containing whitespace go in
double quotes. Missing fields at end-of-line are treated as **-**
(default).

# OPTIONS

**\--dirs** *DIRS*
:   Comma-separated list of directories to scan instead of the
    defaults. Useful for testing (**\--dirs=./test/fixtures**) or
    for staged rollout (**\--dirs=/etc/sysusers.d.new**).

**\--dry-run**
:   Print the actions that would be performed without executing
    them. Reports each entry as **would u foo**, **would g bar**,
    etc.

**-h**, **\--help**
:   Print a usage summary and exit.

# EXIT STATUS

**0**
:   All directives applied successfully (or already existed).

**1**
:   At least one directive failed to apply. The error is written
    to stderr with the file and directive that failed; other
    directives are still attempted.

**2**
:   Bad **\--dirs** value or unrecognised option.

# EXAMPLES

Bootstrap a service account from a package:

    # /usr/lib/sysusers.d/myapp.conf
    u myapp - "My App service" /var/lib/myapp /usr/sbin/nologin
    g myapp -

Apply everything under an alternate directory tree:

    slinit-sysusers --dirs=/staging/sysusers.d

Dry-run before rolling out:

    slinit-sysusers --dry-run

# SEE ALSO

**slinit**(8), **slinit-tmpfiles**(8), **sysusers.d**(5),
**useradd**(8), **groupadd**(8), **systemd-sysusers**(8) — the
systemd counterpart this is modelled after.
