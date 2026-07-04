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

All regexes are POSIX-extended (Go's **regexp**). **--skip-** variants
exclude any entry that matches.

**-f**, **--fstype-regex** *REGEX*  /  **-F**, **--skip-fstype-regex** *REGEX*

**-n**, **--node-regex** *REGEX*  /  **-N**, **--skip-node-regex** *REGEX*

**-o**, **--options-regex** *REGEX*  /  **-O**, **--skip-options-regex** *REGEX*

**-p**, **--point-regex** *REGEX*  /  **-P**, **--skip-point-regex** *REGEX*

Netdev-filter mode (**--netdev** / **--nonetdev**, see below) short-
circuits the **fstype**, **node**, and **options** regex chain — that
matches OpenRC's flag precedence so scripts that combine both keep
the same behaviour.

# NETDEV FILTERS

Whether a mountpoint is a "network device" is decided by looking up
its fstab entry and checking for the **\_netdev** mount option — the
canonical OpenRC convention.

**-e**, **--netdev**
:   Keep only mountpoints flagged **\_netdev** in fstab. Entries not
    present in fstab are excluded.

**-E**, **--nonetdev**
:   Keep only mountpoints present in fstab and NOT flagged
    **\_netdev**.

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

# ENVIRONMENT

**EINFO_QUIET** — when set to a truthy value (**yes**, **1**,
**true**, **on**) all printing is suppressed; the exit code still
reflects whether any entry matched. Same convention as
**slinit-fstabinfo**(8).

# EXIT STATUS

- **0**: at least one entry matched
- **1**: no matches
- **2**: syntax / bad usage

# EXAMPLES

Print every ext4 mountpoint (reverse order):

```
slinit-mountinfo --fstype-regex '^ext4$'
```

Print only local (non-\_netdev) entries:

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
