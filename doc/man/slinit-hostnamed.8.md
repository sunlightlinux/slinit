% SLINIT-HOSTNAMED(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-11-18

# NAME

slinit-hostnamed - hostname and machine information service for slinit

# SYNOPSIS

**slinit-hostnamed** [**-exit-idle** *duration*] [**-fallback-hostname** *name*]

# DESCRIPTION

**slinit-hostnamed** implements *org.freedesktop.hostname1* on the
system bus: the interface GNOME Settings' About panel uses to show and
change the device name, NetworkManager uses to set the hostname,
bluetoothd uses for the Bluetooth name, and GTK uses for the pretty
name. It is systemd-hostnamed's interface, so a desktop built for
systemd works unchanged.

The interface belongs to another project, so upstream's definition is
the contract, not slinit's current behaviour — see **STABILITY.md**.

It reads and writes the same files as **slinit-hostnamectl**(1), with the
same code: */etc/hostname* for the static hostname, */etc/machine-info*
for the pretty hostname, icon name, chassis, deployment, location and
tags, and */etc/os-release* and the DMI tables for the rest.
**slinit-hostnamectl** does not go through this service and works
without a bus.

The kernel's hostname follows systemd's order: the static hostname when
*/etc/hostname* names one, else the transient name a client set with
**SetHostname** (typically NetworkManager, from DHCP), else the default
hostname. The default is *DEFAULT_HOSTNAME* from */etc/os-release*, else
**-fallback-hostname** (*localhost* unless given). slinit-services' boot
script uses its own fallback when */etc/hostname* is missing; setting
*DEFAULT_HOSTNAME* in os-release makes the two agree.

It is started by D-Bus activation and exits after **-exit-idle** without
requests (30 seconds by default; *0* keeps it running).

Hostnames are validated as systemd validates them: letters, digits, '-'
and '.', at most 64 bytes, no empty label and no label starting or ending
with '-'. That is a little different from **slinit-hostnamectl**, which
also refuses *localhost*. Static hostnames with systemd's '?' wildcards
are not expanded.

# OPTIONS

**-exit-idle** *duration*
:   Exit after this long without a request. *0* never exits.

**-fallback-hostname** *name*
:   The hostname to use when neither */etc/hostname* nor os-release's
    *DEFAULT_HOSTNAME* names one.

**--version**
:   Print the version and exit.

# AUTHORIZATION

Every change, and reading the serial number or product UUID, is
authorized by polkit with systemd's action ids, so polkit rules written
for systemd-hostnamed apply as they are:

*org.freedesktop.hostname1.set-hostname*
:   **SetHostname**.

*org.freedesktop.hostname1.set-static-hostname*
:   **SetStaticHostname** and **SetPrettyHostname** (the two change
    together). Implies the actions above and below.

*org.freedesktop.hostname1.set-machine-info*
:   **SetIconName**, **SetChassis**, **SetDeployment**, **SetLocation**,
    **SetTags**, **AddAndRemoveTags**.

*org.freedesktop.hostname1.get-product-uuid*, *org.freedesktop.hostname1.get-hardware-serial*
:   **GetProductUUID**, **GetHardwareSerial**.

*org.freedesktop.hostname1.get-description*
:   Lets **Describe** include the serial number and product UUID. A
    caller without it still gets the rest.

By default each needs an administrator's password (*auth_admin_keep*).
Root is always allowed; when polkitd is not running, nobody else is.

# D-BUS INTERFACE

Bus name *org.freedesktop.hostname1*, object
*/org/freedesktop/hostname1*.

**SetHostname**(*s* hostname, *b* interactive), **SetStaticHostname**(*s*, *b*)
:   Set the transient or the static hostname. An empty static hostname
    removes */etc/hostname*.

**SetPrettyHostname**, **SetIconName**, **SetChassis**, **SetDeployment**, **SetLocation**(*s* value, *b* interactive)
:   Set a */etc/machine-info* field; an empty value removes it. The
    chassis must be one of *desktop*, *laptop*, *convertible*, *server*,
    *tablet*, *handset*, *watch*, *embedded*, *vm* or *container*; the
    icon name must be safe as a file name.

**SetTags**(*as* tags), **AddAndRemoveTags**(*as* add, *as* remove)
:   Replace the machine tags, or remove those whose key matches a glob in
    *remove* and add *add*. Tags are *KEY* or *KEY=VALUE*; one value per
    key, the last winning. Stored colon-separated as *TAGS=*.

**GetProductUUID**(*b* interactive) → *ay*, **GetHardwareSerial**() → *s*
:   From the firmware. *org.freedesktop.hostname1.NoProductUUID* and
    *NoHardwareSerial* when there is none.

**Describe**() → *s*
:   Everything as one JSON object.

**GetMachineInfo**(*s* field) → *s*
:   One raw */etc/machine-info* field;
    *org.freedesktop.hostname1.FieldNotSet* when unset.

Properties, all read-only: **Hostname**, **StaticHostname**,
**PrettyHostname**, **DefaultHostname**, **HostnameSource**, **IconName**,
**Chassis**, **Deployment**, **Location**, **Tags**, **KernelName**,
**KernelRelease**, **KernelVersion**, **OperatingSystemPrettyName**,
**OperatingSystemFancyName**, **OperatingSystemCPEName**,
**OperatingSystemSupportEnd**, **HomeURL**, **OperatingSystemImageID**,
**OperatingSystemImageVersion**, **HardwareVendor**, **HardwareModel**,
**HardwareSKU**, **HardwareVersion**, **FirmwareVersion**,
**FirmwareVendor**, **FirmwareDate**, **MachineID**, **BootID**,
**VSockCID**, **ChassisAssetTag**. The names, the icon, the chassis, the
deployment, the location and the tags are announced with
*PropertiesChanged*. **IconName** falls back to *computer-CHASSIS* and
**Chassis** to what the firmware and virtualization say; the hardware
fields prefer */etc/machine-info*'s *HARDWARE_VENDOR*, *HARDWARE_MODEL*,
*HARDWARE_SKU* and *HARDWARE_VERSION* over DMI. Unknown dates are
*UINT64_MAX*, as systemd reports them.

# FILES

*/usr/share/dbus-1/system.d/org.freedesktop.hostname1.conf*,
*/usr/share/dbus-1/system-services/org.freedesktop.hostname1.service*,
*/usr/share/polkit-1/actions/org.freedesktop.hostname1.policy*
:   Bus policy, activation and polkit actions.

*/etc/hostname*, */etc/machine-info*, */etc/os-release*, */sys/class/dmi/id/*
:   What it reads and writes.

# EXIT STATUS

0 after an idle exit; 1 if the system bus cannot be reached or the name
is already owned.

# SEE ALSO

**slinit-hostnamectl**(1), **slinit-timedated**(8), **machine-info**(5),
**os-release**(5), **polkit**(8)
