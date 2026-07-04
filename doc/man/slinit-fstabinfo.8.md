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
every entry that survives filtering. Positional arguments narrow the
result to a subset of mountpoints; **--fstype** and **--passno**
apply filters ahead of the positional intersection.

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
    (non-zero) and less than *N*; **>***N* the reverse.

**-p**, **--passno** *MOUNTPOINT*
:   In its plain form, prints the **fs_passno** of the specified
    mountpoint. This is the shape init.d scripts use to decide
    fsck order.

# ACTION MODES

**-M**, **--mount**
:   Invoke **mount**(8) for every matching entry, propagating
    exit codes.

**-R**, **--remount**
:   Same as **-M** but with **-o remount** so options can be
    reapplied to an already-mounted filesystem.

# FILTERS

**-t**, **--fstype** *TYPE*[**,***TYPE*...]
:   Keep only entries whose **fs_vfstype** is in the (comma-separated)
    list. Combine with positional arguments to further narrow the
    result.

*MOUNTPOINT*...
:   Positional mountpoints. Without a filter, each is looked up in
    fstab; with a filter, the positional list is intersected against
    the filtered result.

# MISC

**--file** *PATH*
:   Read from *PATH* instead of **/etc/fstab**. Non-standard, useful
    for tests.

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

- **0**: at least one entry matched (and every action succeeded)
- **1**: no matches, or **mount**(8) failed
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

Remount every ext4 entry with the options from fstab:

```
slinit-fstabinfo --fstype ext4 --remount
```

# SEE ALSO

**fstab**(5), **mount**(8), **slinit**(8),
**fstabinfo**(8) (OpenRC).
