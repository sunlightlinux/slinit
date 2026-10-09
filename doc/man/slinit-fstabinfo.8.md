% SLINIT-FSTABINFO(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-07-21

# NAME

slinit-fstabinfo - query /etc/fstab entries (OpenRC-compatible)

# SYNOPSIS

**slinit-fstabinfo** [*OPTIONS*] [*MOUNTPOINT*...]

# DESCRIPTION

**slinit-fstabinfo** is a drop-in replacement for OpenRC's
**fstabinfo**(8): it parses **/etc/fstab** and either prints selected
fields or invokes **mount**(8) on matching entries. It exists so
ported **/etc/init.d** scripts that call **fstabinfo** keep working
under **slinit**.

When no mode flag is given the tool prints the mountpoint field of
every selected entry. Selection follows OpenRC exactly:

1. Each **--fstype** and each **--passno** {**=**|**<**|**>**}*N*, in
   command-line order, scans the whole fstab and appends its matches
   to one list. Several of them therefore select the *union*, not the
   intersection, and an entry matched by two of them is listed (and
   printed, or mounted) twice.
2. Positional *MOUNTPOINT* arguments then keep only the listed entries
   they name. If the list is empty, even because a filter matched
   nothing, the positional arguments become the list instead.
3. With no filter and no positional arguments, every fstab entry is
   selected.

Each selected name is then looked up in fstab; when a mountpoint
appears more than once, the first entry is used.

# OUTPUT MODES

**-b**, **--blockdevice**
:   Print the block-device / spec field (**/dev/sda1**, **UUID=…**,
    **LABEL=…**).

**-o**, **--options**
:   Print the raw mount-options field (**defaults,noatime**).

**-m**, **--mountargs**
:   Print the arguments **mount**(8) would consume:
    **-o** *OPTS* **-t** *TYPE* *SPEC* *MOUNTPOINT*.

**-p**, **--passno** {**=***N* | **<***N* | **>***N*}
:   Filter by **fs_passno**. **=***N* keeps entries whose passno
    equals *N*; **<***N* keeps entries whose passno is present
    (non-zero) and less than *N*; **>***N* the reverse. Entries whose
    mount point is **none** (swap) are always skipped. Matches are added
    to those of any **--fstype** or other **--passno** option; they do
    not narrow them (see DESCRIPTION).

**-p**, **--passno** *MOUNTPOINT*
:   In its plain form, prints the **fs_passno** of the specified
    mountpoint. As in OpenRC this is not a filter: the mountpoint is
    added to the list, and without positional arguments every fstab
    entry follows it, so **--passno /home** prints the passno of
    **/home** and then of each entry. Give the mountpoint positionally
    too (**--passno /home /home**) to print only its passno.

# ACTION MODES

**-M**, **--mount**
:   Invoke **mount**(8) for every matching entry, then print the
    entries as in the default mode. If any **mount** fails the exit
    status is 1.

**-R**, **--remount**
:   Same as **-M** but with **-o remount** so options can be
    reapplied to an already-mounted filesystem.

# SELECTION

**-t**, **--fstype** *TYPE*[**,***TYPE*...]
:   Add entries whose **fs_vfstype** is one of the comma-separated
    types, type by type. Can be repeated, and combines with
    **--passno** as a union.

*MOUNTPOINT*...
:   Positional mountpoints. When the options selected something, keep
    only the selected entries named here; otherwise select exactly
    these mountpoints.

# MISC

**--file** *PATH*
:   Read from *PATH* instead of **/etc/fstab**. Non-standard, useful
    for tests.

**-q**, **--quiet**, **-v**, **--verbose**
:   Accepted for OpenRC compatibility and ignored; use **EINFO_QUIET**
    to suppress output.

**-h**, **--help**
:   Print usage.

**-V**, **--version**
:   Print version string.

# ENVIRONMENT

**EINFO_QUIET** — when set to a truthy value (**yes**, **1**,
**true**, **on**) all printing is suppressed. Actions
(**--mount** / **--remount**) still run and their exit codes still
propagate. This is the OpenRC convention.

# EXIT STATUS

- **0**: something was selected, every selected mountpoint is in
  fstab, and every **mount**(8) succeeded
- **1**: nothing was selected, a selected mountpoint is not in fstab
  (the others are still printed), or the fstab file is missing,
  unparseable or empty
- with **--mount** / **--remount**: the sum of the **mount**(8) exit
  statuses, unless a mountpoint was missing from fstab, which sets
  the status back to 1 (as in OpenRC)
- **2**: syntax / bad usage

# EXAMPLES

Print the block device backing **/**:

```
slinit-fstabinfo --blockdevice /
```

List every fsck pass-1 entry:

```
slinit-fstabinfo --passno =1
```

List every ext4 or xfs entry, plus every fsck pass-1 entry of any
type (an ext4 or xfs entry with pass 1 is listed twice):

```
slinit-fstabinfo --fstype ext4,xfs --passno =1
```

Remount every ext4 entry with the options from fstab:

```
slinit-fstabinfo --fstype ext4 --remount
```

# SEE ALSO

**fstab**(5), **mount**(8), **slinit**(8),
**fstabinfo**(8) (OpenRC).
