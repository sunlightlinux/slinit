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
and */run/sysusers.d/\*.conf* by default. When the same basename
exists in more than one directory, the one in the later directory of
that list wins — so */run* overrides */etc*, unlike systemd, where
*/etc* has the final say. Files are applied in alphabetical order of
their basenames.

The tool shells out to **useradd**(8), **groupadd**(8) and
**gpasswd**(1) for the actual account manipulation, so it inherits
whatever password-database backend those tools use. It is idempotent
by name: a user or group that already exists is left alone, whatever
its UID or GID — a mismatch is not detected or reported.

Typical use is boot-time bootstrap: a package's *.conf* declares the
service account it needs, and the account is present the next time
that service starts. Removes the manual "chown -R after install"
step.

# CONFIG FORMAT

Each line in a *.conf* file is one directive:

    TYPE   NAME    ID   GECOS           HOME         SHELL

Directives:

**u** *name* *uid* *"gecos"* *home* *shell*
:   Create a system user (**useradd --system**). *uid* is a number, or
    **-** to let **useradd** pick one. It is passed to **useradd
    --uid** as given; the *uid*:*gid* form is not split, so declare
    the group with a **g** line instead. Without *home* no home
    directory is created; without *shell* the shell is
    */sbin/nologin*.

**g** *name* *gid*
:   Create a system group. *gid* may be a specific number or **-**.

**m** *user* *group*
:   Add *user* to supplementary group *group* (**gpasswd -a**).

**r** — *lo*-*hi*
:   Accepted and ignored.

Any other type is an error for that line.

Comments start with **#**. Fields containing whitespace go in
double quotes. Missing fields at end-of-line are treated as **-**
(default).

# OPTIONS

**\--dirs** *DIRS*
:   Comma-separated list of directories to scan instead of the
    defaults. Useful for testing (**\--dirs=./test/fixtures**) or
    for staged rollout (**\--dirs=/etc/sysusers.d.new**).

**\--dry-run**
:   Print each entry as **would u foo**, **would g bar**, etc.,
    without executing anything, and without checking whether the
    account already exists.

**-h**, **\--help**
:   Print a usage summary and exit.

# EXIT STATUS

**0**
:   All directives applied successfully (or already existed).

**1**
:   At least one directive failed to apply, or a file could not be
    parsed (that whole file is then skipped). Each error is written to
    stderr; other directives and files are still attempted.

**2**
:   Unrecognised option.

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
