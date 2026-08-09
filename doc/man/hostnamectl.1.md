% HOSTNAMECTL(1) slinit | Sunlight Linux
% Ionut Nechita
% 2026-08-09

# NAME

hostnamectl - control the system hostname (slinit-native, D-Bus-free)

# SYNOPSIS

**hostnamectl** [*OPTIONS*] **status**\
**hostnamectl** [*OPTIONS*] **hostname** [*NAME*]\
**hostnamectl** [*OPTIONS*] **icon-name** [*NAME*]\
**hostnamectl** [*OPTIONS*] **chassis** [*TYPE*]\
**hostnamectl** [*OPTIONS*] **deployment** [*ENV*]\
**hostnamectl** [*OPTIONS*] **location** [*LOCATION*]

The installed binary is **slinit-hostnamectl**; a **hostnamectl**
symlink is provided for systemd muscle memory. Both names dispatch to
the same parser and behave identically.

# DESCRIPTION

**hostnamectl** may be used to query and change the system hostname
and related settings, matching the systemd **hostnamectl**(1) CLI
surface for local operations. Under slinit there is no D-Bus and no
**systemd-hostnamed**: the utility reads and writes the on-disk
sources of truth directly.

Sources of truth:

- **/etc/hostname** — static hostname (also written by *hostname*(1))
- **/etc/machine-info** — pretty hostname, icon name, chassis type,
  deployment environment, physical location, hardware/firmware
  metadata (see **machine-info**(5))
- **/etc/machine-id** — 32-hex-character machine identifier
- **/proc/sys/kernel/random/boot_id** — boot identifier
- **/etc/os-release** — OS pretty name, CPE, home URL
- **/sys/class/dmi/id/\*** — DMI vendor / product / chassis / BIOS
- **uname**(2) — kernel and architecture

Three variants of the hostname coexist:

- **static** — persistent, written to */etc/hostname*
- **transient** — kernel view, set via **sethostname**(2)
- **pretty** — free-form UTF-8, written to */etc/machine-info* under
  **PRETTY_HOSTNAME**

Without a scope flag, setting a hostname updates all three; with
**\--static**, **\--transient**, or **\--pretty**, only the named
variant is written.

# COMMANDS

**status**
:   Show the current hostname and related settings. This is also the
    default when no command is given. Output is a two-column
    "Key: Value" table; empty fields are omitted. With **\--json** or
    **-j**, output is machine-readable JSON with systemd-compatible
    field names.

**hostname** [*NAME*]
:   Without an argument, print the current system hostname (kernel
    view). With *NAME*, set it per the active scope flags. When any
    of **\--static** or **\--transient** is active, *NAME* must be a
    valid POSIX hostname (1..64 chars, alphanumerics, `-` or `.` not
    at start or end, no consecutive dots, not *localhost*).

**icon-name** [*NAME*]
:   Get or set the icon name for the host (freedesktop.org icon
    naming spec, e.g. *computer-laptop*). Setting an empty string
    clears the field.

**chassis** [*TYPE*]
:   Get or set the chassis type. Accepted values: *desktop*, *laptop*,
    *convertible*, *server*, *tablet*, *handset*, *watch*, *embedded*,
    *vm*, *container*. When unset in */etc/machine-info*, **status**
    auto-detects the chassis from the SMBIOS chassis_type and from
    container / hypervisor hints.

**deployment** [*ENV*]
:   Get or set the deployment environment (e.g. *production*,
    *staging*, *devel*).

**location** [*LOCATION*]
:   Get or set the physical location (free-form string, e.g.
    *Bucharest, RO*).

# OPTIONS

**-h**, **\--help**
:   Show usage and exit.

**\--version**
:   Print the slinit version and exit.

**\--transient**
:   Restrict the **hostname** setter to the transient (kernel) name;
    do not touch */etc/hostname* or */etc/machine-info*.

**\--static**
:   Restrict the **hostname** setter to the static name in
    */etc/hostname*; do not touch the kernel view or the pretty name.

**\--pretty**
:   Restrict the **hostname** setter to **PRETTY_HOSTNAME** in
    */etc/machine-info*.

**\--json**=*MODE*
:   Emit output as JSON. *MODE* is *off* (text; the default), *pretty*
    (indented), or *short* (single line).

**-j**
:   Shorthand for **\--json**=*pretty* on a TTY, or **\--json**=*short*
    when stdout is not a TTY.

**\--no-ask-password**
:   Accepted for compatibility with the systemd CLI; slinit-hostnamectl
    never prompts interactively.

# UNSUPPORTED OPTIONS

The following systemd options are accepted at the CLI level (so scripts
that pass them do not fail argument parsing) but return an error at
runtime:

**-H**, **\--host**=[*USER*@]*HOST*
:   Operating on a remote host would require the D-Bus-over-SSH bridge
    that systemd ships. slinit deliberately has no D-Bus dependency.

**-M**, **\--machine**=*CONTAINER*
:   Operating on a *systemd-nspawn* container requires the nspawn
    bus proxy. slinit does not integrate with nspawn.

# EXIT STATUS

**0**
:   Success.

**1**
:   Runtime error (I/O failure, invalid value, unsupported option).

**2**
:   Command-line usage error.

# FILES

*/etc/hostname*
:   Static hostname.

*/etc/machine-info*
:   Pretty hostname, icon name, chassis, deployment, location, and
    hardware/firmware metadata. See **machine-info**(5).

*/etc/machine-id*, */proc/sys/kernel/random/boot_id*
:   Read-only sources for the **Machine ID** and **Boot ID** fields
    in **status** output.

# EXAMPLES

Show the current settings:

    hostnamectl

Set every hostname variant to *ceres*:

    hostnamectl hostname ceres

Set only the pretty name (spaces allowed):

    hostnamectl --pretty hostname 'Ceres VM'

Tag the machine's chassis as a server:

    hostnamectl chassis server

Emit machine-readable JSON:

    hostnamectl --json=short status

# SEE ALSO

**machine-info**(5), **hostname**(1), **hostname**(5),
**sethostname**(2), **os-release**(5)

# NOTES

slinit-hostnamectl is a byte-for-byte independent implementation of
the systemd **hostnamectl**(1) CLI, written in Go, with zero D-Bus
dependency. It shares neither code nor state with **systemd-hostnamed**.
