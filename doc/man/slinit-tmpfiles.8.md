% SLINIT-TMPFILES(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-07-18

# NAME

slinit-tmpfiles - declarative /run and /var bootstrap

# SYNOPSIS

**slinit-tmpfiles** [**\--dirs** *DIRS*] [**\--dry-run**]

# DESCRIPTION

**slinit-tmpfiles** applies **systemd-tmpfiles.d**(5) directives at
boot to create files, directories, symlinks, character/block
devices, and pipes on volatile filesystems (*/run*, */var/run*,
*/tmp*) or persistent ones (*/var*). Reads config from
*/usr/lib/tmpfiles.d/\*.conf*, */etc/tmpfiles.d/\*.conf*, and
*/run/tmpfiles.d/\*.conf* by default with later directories
overriding earlier ones by basename.

Where **slinit-checkpath**(8) is a *repair* tool (fix permissions
on an existing path), **slinit-tmpfiles** is a *creation* tool:
declare a path with mode + owner + type once in a *.conf*, and it
appears at every boot. The two are complementary.

Idempotent by design — running twice produces the same result. On
persistent filesystems where a file already exists at the target
path, most directives (**f**, **d**) leave the content alone and
just fix mode/owner; **F** / **D** truncate and recreate.

# CONFIG FORMAT

Each line is one directive:

    TYPE  PATH  MODE  UID  GID  AGE  ARG

Common directive types (subset shipped in slinit):

**f** *path* *mode* *uid* *gid* *age* *arg*
:   Create a regular file with *arg* as content if the file does
    not exist. Leaves existing files alone.

**F** *path* *mode* *uid* *gid* *age* *arg*
:   Same as **f** but truncates + rewrites if the file already
    exists.

**d** *path* *mode* *uid* *gid* *age*
:   Create a directory. Leaves an existing directory alone (fixes
    mode/owner).

**D** *path* *mode* *uid* *gid* *age*
:   Create a directory + wipe its contents at boot.

**L** *path* — *arg*
:   Create *path* as a symlink pointing at *arg*.

**w** *path* — — — — *arg*
:   Write *arg* into *path* (append). *path* must already exist —
    typically used to poke a sysctl or a proc/sysfs knob.

**e** *path* *mode* *uid* *gid* *age*
:   Adjust an existing path's attributes (mode/uid/gid); do NOT
    create.

Missing fields at end-of-line are treated as **-** (default). Age
is a duration parseable by **time.ParseDuration**; blank / **-**
disables age-based cleanup.

# OPTIONS

**\--dirs** *DIRS*
:   Comma-separated list of directories to scan instead of the
    defaults.

**\--dry-run**
:   Print the actions that would be applied without executing
    them.

**-h**, **\--help**
:   Print a usage summary and exit.

# EXIT STATUS

**0**
:   All directives applied successfully.

**1**
:   At least one directive failed. The error is written to stderr
    with the failing file + directive; other directives are still
    attempted.

**2**
:   Bad **\--dirs** value or unrecognised option.

# EXAMPLES

Bootstrap a service's runtime directory at boot:

    # /usr/lib/tmpfiles.d/myapp.conf
    d /run/myapp        0755 myapp myapp -
    f /run/myapp/state  0644 myapp myapp - initial-content
    L /var/log/myapp    -    -     -     - /var/log/myapp.d/current

Poke a sysctl-style knob without shelling out:

    w /proc/sys/net/core/rmem_max - - - - 8388608

Apply everything under an alternate tree (staging):

    slinit-tmpfiles --dirs=/staging/tmpfiles.d

# SEE ALSO

**slinit**(8), **slinit-sysusers**(8), **slinit-checkpath**(8),
**tmpfiles.d**(5), **systemd-tmpfiles**(8) — the systemd
counterpart this is modelled after.
