% SLINIT-TMPFILES(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-07-18

# NAME

slinit-tmpfiles - declarative /run and /var bootstrap

# SYNOPSIS

**slinit-tmpfiles** [**\--dirs** *DIRS*] [**\--dry-run**]

# DESCRIPTION

**slinit-tmpfiles** applies a subset of **systemd-tmpfiles.d**(5)
directives at boot to create and adjust files, directories and
symlinks on volatile filesystems (*/run*, */tmp*) or persistent ones
(*/var*). Reads config from */etc/tmpfiles.d/\*.conf*,
*/run/tmpfiles.d/\*.conf* and */usr/lib/tmpfiles.d/\*.conf* by
default. When the same basename exists in more than one directory,
*/etc* overrides */run*, which overrides */usr/lib* (as in systemd).
The surviving files from all directories are applied in alphabetical
order of their basenames.

Where **slinit-checkpath**(8) is a *repair* tool (fix permissions
on an existing path), **slinit-tmpfiles** is a *creation* tool:
declare a path with mode + owner + type once in a *.conf*, and it
appears at every boot. The two are complementary.

# CONFIG FORMAT

Each line is one directive:

    TYPE  PATH  MODE  UID  GID  AGE  ARG

Fields may be double-quoted to contain spaces. Missing fields, or
**-**, take the defaults: mode **0644** (for directories too), owner
**root** (UID 0) and group GID 0. *UID* and *GID* may be numbers or
names; names are looked up in */etc/passwd* and */etc/group* directly,
and an unknown name makes the whole file fail to parse. *AGE* is
accepted and ignored — no age-based cleanup is performed. Type
modifiers (**!**, **+**, **=**, **-**) are accepted and ignored.

For **f**, **F** and **w**, *ARG* may contain C-style escapes: **\\a**,
**\\b**, **\\f**, **\\n**, **\\r**, **\\t**, **\\v**, **\\\\**,
**\\"**, **\\'**, **\\s** (space), **\\x** plus two hex digits, and
**\\** plus three octal digits. An invalid escape makes the whole file
fail to parse.

Supported types:

**f** *path* *mode* *uid* *gid* *age* *arg*
:   Create a regular file if it does not exist, writing *arg* (if
    given) into it; if it exists, only set its mode and owner — its
    content is left alone.

**F** *path* *mode* *uid* *gid* *age* *arg*
:   Create the file, or truncate it to zero length if it exists, write
    *arg* (if given), then set the owner.

**d** *path* *mode* *uid* *gid* *age*
:   Create the directory, and any missing parents, then set its mode
    and owner (also when it already exists).

**D** *path* *mode* *uid* *gid* *age*
:   Remove the directory and everything in it, then create it as
    **d** does.

**L** *path* — — — — *arg*
:   Create *path* as a symlink pointing at *arg*. An existing *path*
    is left untouched.

**w** *path* — — — — *arg*
:   Write *arg* to *path*, replacing its contents (the file is created
    with mode 0644 if missing) — typically used to poke a proc or
    sysfs knob.

**r** *path*, **R** *path*
:   Remove *path*; **R** removes a directory recursively. A missing
    path is not an error.

**z** *path* *mode* *uid* *gid*, **Z** *path* *mode* *uid* *gid*
:   Set the mode and owner of an existing path; **Z** applies them
    recursively to everything below a directory.

Any other type is an error for that line.

# OPTIONS

**\--dirs** *DIRS*
:   Comma-separated list of directories to scan instead of the
    defaults, lowest precedence first: for the same basename, a later
    directory in *DIRS* overrides an earlier one. The default is
    equivalent to
    **\--dirs=/usr/lib/tmpfiles.d,/run/tmpfiles.d,/etc/tmpfiles.d**.

**\--dry-run**
:   Print each entry as **would** *TYPE* *PATH* without executing
    anything.

**-h**, **\--help**
:   Print a usage summary and exit.

# EXIT STATUS

**0**
:   All directives applied successfully.

**1**
:   At least one directive failed, or a file could not be parsed (that
    whole file is then skipped). Each error is written to stderr;
    other directives and files are still attempted.

**2**
:   Unrecognised option.

# EXAMPLES

Bootstrap a service's runtime directory at boot:

    # /usr/lib/tmpfiles.d/myapp.conf
    d /run/myapp        0755 myapp myapp -
    f /run/myapp/state  0644 myapp myapp -
    L /var/log/myapp    -    -     -     - /var/log/myapp.d/current

Poke a sysctl-style knob without shelling out:

    w /proc/sys/net/core/rmem_max - - - - 8388608

Apply everything under an alternate tree (staging):

    slinit-tmpfiles --dirs=/staging/tmpfiles.d

# SEE ALSO

**slinit**(8), **slinit-sysusers**(8), **slinit-checkpath**(8),
**tmpfiles.d**(5), **systemd-tmpfiles**(8) — the systemd
counterpart this is modelled after.
