% SLINIT-HOSTNAMECTL(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-10-03

# NAME

slinit-hostnamectl - query or change the system hostname, without D-Bus

# SYNOPSIS

**slinit-hostnamectl** [*OPTIONS*...] *COMMAND* [*ARGUMENT*]

# DESCRIPTION

**slinit-hostnamectl** matches systemd's **hostnamectl**(1) command
surface so existing habits and scripts keep working, and reaches the same
settings a different way: it reads and writes the on-disk sources of
truth directly. There is no D-Bus and no **systemd-hostnamed** to be
running.

* */etc/hostname* — the static hostname, and the transient one via
  **sethostname**(2)
* */etc/machine-info* — pretty name, icon, chassis, deployment, location

With no *COMMAND*, **status** is implied.

# COMMANDS

**status**
:   Show every hostname setting together with the host metadata.

**hostname** [*NAME*]
:   Print the hostname, or set it. Which of the three is written depends
    on the scope flags below. With none of them, all three are set —
    kernel, */etc/hostname* and the pretty name — except that the pretty
    name is left alone when *NAME* is what would have been inferred from
    it anyway, so setting a hostname does not overwrite a deliberately
    chosen pretty one with a duplicate of itself.

**icon-name** [*NAME*]
:   Get or set the icon name recorded for this host.

**chassis** [*TYPE*]
:   Get or set the chassis type — *desktop*, *laptop*, *server*, *tablet*,
    *handset*, *vm*, *container* and so on.

**deployment** [*ENV*]
:   Get or set the deployment environment, conventionally *development*,
    *integration*, *staging* or *production*.

**location** [*LOCATION*]
:   Get or set a free-form physical location.

# OPTIONS

**\--transient**, **\--static**, **\--pretty**
:   Restrict **hostname** to one of the three: **\--transient** calls
    **sethostname**(2) and touches no file, **\--static** writes
    */etc/hostname* and does not touch the kernel, **\--pretty** writes
    only *PRETTY_HOSTNAME* in */etc/machine-info*. The transient name
    lasts until reboot; the other two persist.

    The scope flags apply to **hostname** alone. **icon-name**,
    **chassis**, **deployment** and **location** only ever write
    */etc/machine-info*.

**\--json**=*off*|*pretty*|*short*, **-j**
:   Machine-readable output. **-j** is *pretty* on a terminal and *short*
    otherwise.

**\--no-ask-password**
:   Accepted and does nothing. There is no password prompt to suppress —
    the files are written directly, so ordinary filesystem permissions
    decide.

**-h**, **\--help**, **\--version**
:   Help, and the slinit version.

## Accepted but not supported

**-H**, **\--host**=[*USER*@]*HOST* and **-M**,
**\--machine**=*CONTAINER* are parsed so a command line copied from a
systemd system does not fail on an unknown flag, but they do nothing:
both are D-Bus mechanisms — over SSH, and into an **nspawn** container —
and this program has no D-Bus. Operate on the remote or the container
directly instead.

# EXIT STATUS

**0** on success. Non-zero if a file could not be read or written, or the
command line was not understood.

# EXAMPLES

    slinit-hostnamectl status
    slinit-hostnamectl hostname web-01
    slinit-hostnamectl --static hostname web-01.example.net
    slinit-hostnamectl --pretty hostname "Ionut's laptop"
    slinit-hostnamectl chassis laptop
    slinit-hostnamectl --json=short status

# FILES

*/etc/hostname*
:   The static hostname. One line.

*/etc/machine-info*
:   *PRETTY_HOSTNAME*, *ICON_NAME*, *CHASSIS*, *DEPLOYMENT*, *LOCATION*
    as shell-style assignments.

# SEE ALSO

**slinit**(8), **slinit-timedatectl**(8), **slinitctl**(8),
**hostnamectl**(1), **hostname**(5), **machine-info**(5)
