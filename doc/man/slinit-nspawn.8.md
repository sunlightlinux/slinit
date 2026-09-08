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
   (optionally **network** too — see **--private-network**).
2. **pivot_root**(2)s into *ROOTFS* so the container sees it as **/**.
3. Mounts a fresh */proc*, */sys*, */dev* inside.
4. **exec**(2)s */sbin/slinit* (or **--init=**\ *PATH*) as PID 1 inside
   the new PID namespace.
5. Registers *NAME* → *container-PID* in */run/slinit/machines/* so
   **slinit-machinectl**(8) sees it and **slinit-journalctl -M** *NAME*
   can reach its journal.

Contrast with **systemd-nspawn**(1): the design is deliberately
smaller. There is no D-Bus, no networkd/resolved bridge, no
image-format probing, no OS-tree integration. The container is a
plain rootfs directory that runs slinit as PID 1; the host's slinit
tracks it via the same machine registry it uses for any other
namespaced process.

Requires **CAP_SYS_ADMIN** and **CAP_SYS_CHROOT** (typically root).

# FLAGS

**--name** *NAME*
:   Container name; used as the registry key under
    */run/slinit/machines/* and as the default UTS hostname. Required.

**--boot** *ROOTFS*
:   Path to the container's root filesystem — the directory that
    **pivot_root**(2) targets. Must contain at least */sbin/slinit*
    (or whatever **--init** points at) plus the shared-library
    graph the init needs. Required.

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
    Passed to **slinit-machinectl register** internally.

**--** *INIT-ARGS*...
:   Everything after **--** is passed verbatim to the container's
    init as argv, after argv[0]. Use this to hand *slinit* its own
    flags (**-d** service dir override, **--services-dir=**, etc.).

**-h**, **--help**
:   Show flag summary.

# ENVIRONMENT

Inside the container, slinit-nspawn preserves the host's environment
verbatim by default. The container's own service files control what
each service actually sees at exec time via **env-file** /
**env-var** directives (see **slinit-service**(5)).

# EXIT STATUS

**slinit-nspawn** itself does not wait for the container to exit —
after **exec**(2)ing the container's init it goes away. Exit status
comes from the pre-exec setup:

**0**
:   Container launched successfully; PID recorded in the registry;
    ready.

**1**
:   Setup error (namespace unshare failed, pivot_root failed,
    registry write failed, init binary missing inside rootfs).

**2**
:   Usage error (missing required flag, invalid path).

Container exit status is observable from the host via the machine's
PID in the registry (waitpid-style, or by watching
*/run/slinit/machines/*\ *NAME*'s liveness through
**slinit-machinectl status**).

# FILES

*ROOTFS*/sbin/slinit
:   The default init binary inside the container. Override with
    **--init**.

*/run/slinit/machines/*\ *NAME*
:   Registry entry created on launch. Removed with
    **slinit-machinectl unregister**.

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
