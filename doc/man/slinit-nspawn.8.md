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
   read-only */sys*, *devpts* on */dev/pts*, and tmpfs on */run* and
   */tmp*. */dev* itself is not populated: the rootfs must provide its
   device nodes. A mount that fails is reported and skipped.
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
**_SLINIT_NSPAWN_INIT**, **_SLINIT_NSPAWN_HOSTNAME** and
**_SLINIT_NSPAWN_INIT_ARGS** that carry the launch parameters into the
child. The container's own service files control what each service
actually sees at exec time via **env-file** and related directives
(see **slinit-service**(5)).

# EXIT STATUS

**slinit-nspawn** waits for the container. Once the container's init
has started, the exit status is the container's own.

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
