% SLINIT-FSTAB-DECODE(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-10-02

# NAME

slinit-fstab-decode - run a command with fstab-escaped arguments decoded

# SYNOPSIS

**slinit-fstab-decode** *COMMAND* [*ARGUMENT* ...]

# DESCRIPTION

**slinit-fstab-decode** is a drop-in replacement for sysvinit's
**fstab-decode**(8). It decodes the escapes in its *ARGUMENT*s and then
becomes *COMMAND*.

Mount tables escape the characters that would otherwise split a field, so
a mount point containing a space appears in */etc/fstab* and
*/proc/mounts* as `/mnt/my\040disk`. Anything that pulls a field out of
those tables and hands it to **umount**(8) has to undo that first:

    awk '$3 == "nfs" { print $2 }' /etc/fstab | xargs slinit-fstab-decode umount

**slinit-fstabinfo**(8) is the neighbour that *queries* fstab;
this one only unescapes and execs, and is what an init.d script already
calls.

## The escape table

Five entries, and that is the whole table:

| escape | decodes to |
|---|---|
| `\\` | a backslash |
| `\011` | tab |
| `\012` | newline |
| `\040` | space |
| `\134` | a backslash |

It is **not** a general octal parser, and that is deliberate rather than
an omission: `\101` stays `\101` instead of becoming `A`. Mount tables
only ever escape those five characters, and decoding more would corrupt a
path that genuinely contains a backslash followed by digits. An escape
the table does not know keeps its backslash — `\x` stays `\x` — and a
trailing lone backslash is left alone.

Only the *ARGUMENT*s are decoded. *COMMAND* is not: it is a path to look
up, not a field out of a mount table.

## Becoming the command

The command replaces this process rather than running as a child of it,
as **execvp**(3) does upstream. That is not an implementation detail: the
caller's shell has one pid to wait on and signal, the exit status needs no
relaying, and an **xargs**(1) pipeline behaves exactly as it does with
sysvinit's version.

# EXIT STATUS

**127**
:   *COMMAND* could not be run — not found, or not executable.

**1**
:   No *COMMAND* was given.

*anything else*
:   Whatever *COMMAND* returned. This program adds nothing to it.

# EXAMPLES

Unmount every NFS filesystem listed in fstab, mount points with spaces
included:

    awk '$3 == "nfs" { print $2 }' /etc/fstab | xargs slinit-fstab-decode umount

Print the mount points of every VFAT filesystem:

    awk '$3 == "vfat" { print $2 }' /etc/fstab | xargs slinit-fstab-decode echo

# SEE ALSO

**slinit-fstabinfo**(8), **slinit-mountinfo**(8), **slinit**(8),
**fstab-decode**(8), **fstab**(5), **umount**(8)
