% SLINIT-MOUNTINFO(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-07-21

# NAME

slinit-mountinfo - query the kernel mount table (OpenRC-compatible)

# SYNOPSIS

**slinit-mountinfo** [*OPTIONS*] [*MOUNTPOINT*...]

# DESCRIPTION

**slinit-mountinfo** is a drop-in replacement for OpenRC's
**mountinfo**(8). It reads **/proc/mounts** — the kernel's view of
currently-mounted filesystems — applies regex, netdev, and
positional filters, and prints one field per matching entry.

Rows are printed in reverse order (deepest / most-recent first).
This is the order init.d scripts want for **umount** sequencing, and
matches OpenRC's behaviour.

Fields are printed exactly as they appear in **/proc/mounts**, as
OpenRC prints them: the kernel's octal escapes in the mountpoint and
device fields (**\\040** space, **\\011** tab, **\\012** newline,
**\\134** backslash, and **\\043** **#** in the device) are kept,
so every entry is one line and one word. Matching works on the
decoded value: the positional argument **"/mnt/my share"** and
**--point-regex 'my share'** both select the mount printed as
**/mnt/my\\040share**.

The pseudo entry with **rootfs** as its filesystem type is always
skipped, matching the C original.

# OUTPUT SELECTORS

Only one may be given; the last one on the command line wins. Default
is the mountpoint.

**-i**, **--options**
:   Print the mount-options field.

**-s**, **--fstype**
:   Print the filesystem type.

**-t**, **--node**
:   Print the device / spec field (**/dev/…**, **UUID=…**, or a
    network share URL).

# REGEX FILTERS

All regexes use Go's **regexp** (RE2) syntax, which for ordinary
patterns reads like POSIX extended regular expressions; back-references
are not supported. Patterns are unanchored. **--skip-** variants
exclude any entry that matches.

**-f**, **--fstype-regex** *REGEX*  /  **-F**, **--skip-fstype-regex** *REGEX*

**-n**, **--node-regex** *REGEX*  /  **-N**, **--skip-node-regex** *REGEX*

**-o**, **--options-regex** *REGEX*  /  **-O**, **--skip-options-regex** *REGEX*

**-p**, **--point-regex** *REGEX*  /  **-P**, **--skip-point-regex** *REGEX*

When a netdev filter (**--netdev** / **--nonetdev**, see below)
applies to an entry, it alone decides and the **fstype**, **node**
and **options** regexes are skipped for that entry; when it does not
apply, the regexes are used as usual. This is OpenRC's precedence.

# NETDEV FILTERS

Whether a mountpoint is a "network device" is decided by looking up
its fstab entry and checking for the **\_netdev** mount option — the
canonical OpenRC convention.

The filter applies to an entry only when its mountpoint is listed in
fstab, or when positional mountpoints were given. A mount that fstab
does not know about is otherwise *not* filtered out: it falls through
to the regex filters, exactly as in OpenRC's **mountinfo.c**. So
**--netdev** with no positional arguments prints the **\_netdev**
entries *and* every mount absent from fstab (**/proc**, **/run**, …);
give **--fstype-regex** or the mountpoints to narrow it.

**-e**, **--netdev**
:   Drop mounts that fstab lists without **\_netdev**. With positional
    mountpoints, also drop those fstab does not list.

**-E**, **--nonetdev**
:   Drop mounts that fstab lists with **\_netdev**. With positional
    mountpoints, also drop those fstab does not list.

# POSITIONAL FILTER

Positional arguments must be absolute paths. Each is canonicalised
through symlink resolution before matching, so a symlinked pathspec
still matches its target in the mount table.

# TEST SEAMS

Non-standard, but useful for automated testing:

**--proc-mounts** *PATH*
:   Read from *PATH* instead of **/proc/mounts**.

**--fstab** *PATH*
:   Read the fstab from *PATH* instead of **/etc/fstab** (only used
    when a netdev filter is active).

# MISC

**-q**, **--quiet**
:   Print nothing; the exit status still reports whether any entry
    matched, so **slinit-mountinfo -q /proc && …** is a mount test.
    Same as OpenRC, where **-q** sets **EINFO_QUIET**.

**-v**, **--verbose**
:   Accepted for OpenRC compatibility and ignored.

# ENVIRONMENT

**EINFO_QUIET** — when set to a truthy value (**yes**, **1**,
**true**, **on**) all printing is suppressed, as with **-q**; the
exit code still reflects whether any entry matched. OpenRC's
**mountinfo** honours an inherited **EINFO_QUIET** and so does this
tool. (**slinit-fstabinfo**(8) differs: like OpenRC's **fstabinfo**
it ignores the inherited variable and is silenced only by **-q**.)

# EXIT STATUS

- **0**: at least one entry matched
- **1**: no matches, or the mount table (or, with a netdev filter, an
  existing fstab) could not be read
- **2**: syntax / bad usage, including an invalid regex or a relative
  positional path

# EXAMPLES

Print every ext4 mountpoint (reverse order):

```
slinit-mountinfo --fstype-regex '^ext4$'
```

Print local mounts: every mount except those fstab tags
**\_netdev** (mounts absent from fstab are included, as in OpenRC):

```
slinit-mountinfo --nonetdev
```

Print the block device backing **/home**:

```
slinit-mountinfo --node /home
```

# SEE ALSO

**mount**(8), **proc**(5), **slinit-fstabinfo**(8), **slinit**(8),
**mountinfo**(8) (OpenRC).
