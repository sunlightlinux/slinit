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
config from */etc/sysusers.d/\*.conf*, */run/sysusers.d/\*.conf*,
and */usr/lib/sysusers.d/\*.conf* by default. When the same basename
exists in more than one directory, */etc* overrides */run*, which
overrides */usr/lib* (as in systemd). The surviving files from all
directories are read in alphabetical order of their basenames.

As with **systemd-sysusers**, every file is read before anything is
created; then all groups are created, then all users, then all
memberships. A line may therefore name a group or user declared
later in the same file or in a later file. When the same user (or
the same group) is declared more than once, the first declaration
wins; a later one that differs is ignored with a warning naming
both lines.

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

**u** *name* *uid*[:*gid*] *"gecos"* *home* *shell*
:   Create a system user (**useradd --system**). *uid* is a number, or
    **-** to let **useradd** pick one (passed to **useradd --uid**).
    With the *uid*:*gid* form, *gid* (a number or a group name) becomes
    the user's primary group (**useradd --gid**); that group must
    already exist or be declared by a **g** line (or be the group of
    another **u** line) anywhere in the configuration. Without *gid*,
    a group named *name* — existing or declared by a **g** line —
    becomes the primary group, as with **systemd-sysusers**;
    otherwise **useradd** creates it. Without *home* no home directory is
    created; without *shell* the shell is */sbin/nologin*.

**g** *name* *gid*
:   Create a system group. *gid* may be a specific number or **-**.

**m** *user* *group*
:   Add *user* to supplementary group *group* (**gpasswd -a**).
    If no **u** line declares *user*, it is created as with
    **u** *user* **-**; if no **g** or **u** line declares *group*,
    it is created as with **g** *group* **-** (as with
    **systemd-sysusers**).

**r** — *lo*-*hi*
:   Accepted and ignored.

Any other type is an error for that line.

Comments start with **#**. Fields containing whitespace go in
double quotes. Missing fields at end-of-line are treated as **-**
(default).

# OPTIONS

**\--dirs** *DIRS*
:   Comma-separated list of directories to scan instead of the
    defaults, lowest precedence first: for the same basename, a later
    directory in *DIRS* overrides an earlier one. The default is
    equivalent to
    **\--dirs=/usr/lib/sysusers.d,/run/sysusers.d,/etc/sysusers.d**.
    Useful for testing (**\--dirs=./test/fixtures**) or
    for staged rollout (**\--dirs=/etc/sysusers.d.new**).

**\--dry-run**
:   Print each entry as **would u foo**, **would g bar**, etc., in
    the order they would be applied (including users and groups
    implied by **m** lines), without executing anything, and without checking whether the
    account already exists.

**-h**, **\--help**
:   Print a usage summary and exit.

**\--version** (since 3.7.0)
:   Print the version and exit. Recognised only as the sole argument.

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
