% SLINIT-NSPAWN(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-09-08

# NAME

slinit-nspawn - launch a slinit container in a new set of Linux namespaces

# SYNOPSIS

**slinit-nspawn** **--name** *NAME* **--boot** *ROOTFS* [*flags*] [**--** *INIT-ARGS*...]

# DESCRIPTION

**slinit-nspawn** launches *ROOTFS* as a slinit container. It:

1. Creates fresh **PID**, **mount**, **UTS**, and **IPC** namespaces
   (optionally **network** too — see **--private-network**) for a child
   process, and registers *NAME* → *child-PID* in
   */run/slinit/machines/* so **slinit-machinectl**(8) sees it and
   **slinit-journalctl -M** *NAME* can reach its journal.
2. In the child: sets the hostname, **pivot_root**(2)s into *ROOTFS* so
   the container sees it as **/**, and mounts a fresh */proc*, a
   read-only */sys*, a new tmpfs on */dev*, a new *devpts* instance on
   */dev/pts*, and tmpfs on */dev/shm*, */run* and */tmp*. A mount that
   fails is reported and skipped, except */dev* and its device nodes.
   See **ISOLATION**.
3. **exec**(2)s */sbin/slinit* (or **--init=**\ *PATH*) as PID 1 inside
   the new PID namespace.
4. Stays in the foreground: SIGTERM and SIGINT are forwarded to the
   container's init, and when the container exits **slinit-nspawn**
   removes the registry entry and exits with the container's status.

Contrast with **systemd-nspawn**(1): the design is deliberately
smaller. There is no D-Bus, no networkd/resolved bridge, no
image-format probing, no OS-tree integration. The container is a
plain rootfs directory that runs slinit as PID 1; the host's slinit
tracks it via the same machine registry it uses for any other
namespaced process.

Must be run as root (effective UID 0); this is checked before anything
is created.

# ISOLATION

The rootfs is mounted **nodev** (since 4.0.3), recursively, so a device
node shipped in the image cannot be opened. It is not **nosuid**: the
container's setuid binaries work. **systemd-nspawn**(1) leaves the root
**dev** and closes devices with a device cgroup instead; **slinit-nspawn**
sets up no cgroup, so the mount flag stands in for it.

*/dev* is a fresh **nodev** tmpfs (since 4.0.3; it used to be the
image's own */dev*), as under **systemd-nspawn**(1). It holds only
*null*, *zero*, *full*, *random*, *urandom* and *tty*, bind-mounted
from the host; the */dev/fd*, */dev/stdin*, */dev/stdout* and
*/dev/stderr* links; */dev/ptmx* pointing at *pts/ptmx*; and the mount
points for */dev/pts* and */dev/shm*. There is no */dev/console*: the
container's init keeps the terminal **slinit-nspawn** was started on.

*/proc/sys*, */proc/acpi*, */proc/apm*, */proc/asound*, */proc/bus*,
*/proc/fs*, */proc/irq* and */proc/scsi* are read-only, and
*/proc/kallsyms*, */proc/kcore*, */proc/keys*, */proc/sysrq-trigger* and
*/proc/timer_list* are covered with */dev/null* (since 4.0.3), as
**systemd-nspawn**(1) makes them read-only and inaccessible: a writable
*/proc/sys* let the container set host-wide tunables such as
*kernel.core_pattern*, which runs a program as host root. With
**--private-network**, */proc/sys/net* stays writable, since those
settings belong to the container's own network namespace. */sys* is
read-only.

The container's init runs with **systemd-nspawn**(1)'s default
capability set (since 4.0.3; it had every capability): CAP_AUDIT_CONTROL,
CAP_AUDIT_WRITE, CAP_CHOWN, CAP_DAC_OVERRIDE, CAP_DAC_READ_SEARCH,
CAP_FOWNER, CAP_FSETID, CAP_IPC_OWNER, CAP_KILL, CAP_LEASE,
CAP_LINUX_IMMUTABLE, CAP_MKNOD, CAP_NET_BIND_SERVICE, CAP_NET_BROADCAST,
CAP_NET_RAW, CAP_SETFCAP, CAP_SETGID, CAP_SETPCAP, CAP_SETUID,
CAP_SYS_ADMIN, CAP_SYS_BOOT, CAP_SYS_CHROOT, CAP_SYS_NICE,
CAP_SYS_PTRACE, CAP_SYS_RESOURCE and CAP_SYS_TTY_CONFIG. The others,
CAP_SYS_MODULE, CAP_SYS_RAWIO, CAP_SYS_TIME, CAP_NET_ADMIN,
CAP_MAC_ADMIN, CAP_IPC_LOCK and the rest, are dropped from the bounding
and inheritable sets, so nothing in the container can regain them.
With **--private-network** CAP_NET_ADMIN is kept as well, to configure
the container's own network stack. There is no flag to retain more.

*/proc* and */sys* are created before **pivot_root**(2), so that they
also mount when **slinit-nspawn** itself runs inside a user namespace.

# FLAGS

**--name** *NAME*
:   Container name; used as the registry key under
    */run/slinit/machines/* and as the default UTS hostname. Required.

**--boot** *ROOTFS*
:   Path to the container's root filesystem — the directory that
    **pivot_root**(2) targets; it must be an existing directory. Must
    contain at least */sbin/slinit* (or whatever **--init** points at)
    plus the shared-library graph the init needs. Required. When the
    init cannot be executed, the error says whether the file or its
    ELF interpreter is missing.

**--init** *PATH*
:   Init binary to **exec**(2) as PID 1 inside the container.
    Defaults to */sbin/slinit*.

**--hostname** *NAME*
:   Container's UTS hostname. Defaults to **--name**.

**--private-network**
:   Add **CLONE_NEWNET** to the namespace set so the container gets
    a completely empty network stack (no interfaces except loopback,
    which the container's own userspace must bring up). Without this
    flag the container shares the host's network namespace.

**--machine-dir** *DIR*
:   Override the registry directory (default */run/slinit/machines/*).
    A failure to write the entry is reported but does not stop the
    container.

**--** *INIT-ARGS*...
:   Everything after **--** is passed verbatim to the container's
    init as argv, after argv[0]. Use this to hand *slinit* its own
    flags (**-d** service dir override, **--services-dir=**, etc.).

**-h**
:   Show flag summary.

# ENVIRONMENT

The container's init inherits the environment **slinit-nspawn** was
started with, plus the internal variables **_SLINIT_NSPAWN_CHILD**,
**_SLINIT_NSPAWN_NAME**, **_SLINIT_NSPAWN_ROOTFS**,
**_SLINIT_NSPAWN_INIT**, **_SLINIT_NSPAWN_HOSTNAME**,
**_SLINIT_NSPAWN_INIT_ARGS** and, with **--private-network**,
**_SLINIT_NSPAWN_PRIVATE_NET** that carry the launch parameters into the
child. The container's own service files control what each service
actually sees at exec time via **env-file** and related directives
(see **slinit-service**(5)).

# EXIT STATUS

**slinit-nspawn** waits for the container. Once the container's init
has started, the exit status is the container's own: its init's exit
code, or 128 plus the signal number when the init was killed by a
signal (since 4.0.3; it was 255).

**1**
:   Setup error before or during the launch: bad or missing flags
    (**--name** and **--boot** are required), *ROOTFS* is not a
    directory, not running as root, the namespaces could not be
    created, or the child failed to set up the container or execute
    the init.

# FILES

*ROOTFS*/sbin/slinit
:   The default init binary inside the container. Override with
    **--init**.

*/run/slinit/machines/*\ *NAME*
:   Registry entry created on launch and removed when the container
    exits. If **slinit-nspawn** itself is killed, remove a leftover
    entry with **slinit-machinectl unregister**.

# EXAMPLES

Launch a container from a prepared Alpine minirootfs (see the
*demo/alpine-nspawn/* walkthrough in the repo):

    slinit-nspawn --name alpine-demo --boot /var/lib/machines/alpine \
        --private-network

Launch a container that shares the host's network stack (bridge /
routed containers), naming it *worker-01*:

    slinit-nspawn --name worker-01 --boot /var/lib/machines/worker

Pass extra flags to the container's slinit:

    slinit-nspawn --name minimal --boot /tmp/tiny -- \
        --services-dir=/etc/slinit.d --banner=""

Stream the container's journal from the host:

    slinit-journalctl -M alpine-demo -f

# SEE ALSO

**slinit-machinectl**(8), **slinit-journalctl**(8),
**slinit**(8), **slinitctl**(8), **systemd-nspawn**(1)

The *demo/alpine-nspawn/* directory in the source tree walks
end-to-end from empty rootfs to running container.
