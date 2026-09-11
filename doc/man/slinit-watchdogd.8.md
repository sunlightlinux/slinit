% SLINIT-WATCHDOGD(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-09-11

# NAME

slinit-watchdogd - runtime hardware watchdog petting daemon

# SYNOPSIS

**slinit-watchdogd** [**-d** *DEVICE*] [**-t** *TIMEOUT_SEC*] [**-i** *INTERVAL_SEC*] [**-v**]

# DESCRIPTION

**slinit-watchdogd** opens the kernel watchdog character device,
sets a bounded reset countdown, and calls *WDIOC_KEEPALIVE* at
half the timeout interval so the WDT peripheral stays quiet
during normal operation. If **slinit-watchdogd** itself hangs or
gets scheduled out for longer than *TIMEOUT_SEC*, the WDT fires
and the kernel triggers a hardware reset — the classic
last-resort "the system is wedged, reboot me" safety net for
embedded and server deployments.

finit-parity for **finit**'s built-in **watchdog.c** loop.
Complements the shutdown-time WDT arming already shipped in
**pkg/shutdown** (activated via **slinit.reboot-watchdog** on
the kernel command line): together they cover the full runtime-
plus-shutdown WDT lifecycle a real embedded deployment needs.

# OPTIONS

**-d** *DEVICE*
:   Path to the watchdog character device.
    Default */dev/watchdog*.

**-t** *TIMEOUT_SEC*
:   Reset countdown in seconds, applied to the driver via
    *WDIOC_SETTIMEOUT* at startup. Kernel drivers typically
    cap this at 60-127 s depending on the specific peripheral;
    an out-of-range value is rejected by the kernel with
    *EINVAL*. Default 60 s.

**-i** *INTERVAL_SEC*
:   Petting interval in seconds. Zero (default) computes
    *TIMEOUT_SEC / 2* so the WDT sees two pets before its
    window closes. Values below 5 s are clamped up to 5;
    values greater than or equal to *TIMEOUT_SEC* are clamped
    down to *TIMEOUT_SEC - 1* to guarantee at least one pet
    per window.

**-v**
:   Verbose mode — logs every *WDIOC_KEEPALIVE* call at Info
    level to stderr. Default is quiet (only startup + exit
    lines).

# SIGNALS

*SIGTERM*, *SIGINT*
:   Graceful shutdown. Writes the *V* magic-close byte to
    */dev/watchdog*, then **close**(2)s the fd. The kernel
    driver disarms the WDT; the system does NOT reset.

*SIGPWR*
:   Hand-over to a successor watchdog daemon. **close**(2) the
    fd WITHOUT the magic byte — the WDT stays armed and the
    successor must **open**(2) the device within the current
    timeout window or the WDT fires. finit-parity for its
    handover semantics.

*SIGHUP*
:   Re-arm at the current *TIMEOUT_SEC*. Rare — useful when an
    operator wants to reset the countdown after a scripted
    intervention without restarting the daemon.

# EXAMPLES

Basic slinit service pinning the WDT with the defaults:

    watchdog-pet {
        type = process
        command = /sbin/slinit-watchdogd
        restart = yes
        restart-limit-count = 0
        depends-on: system-init
    }

Short-window embedded deployment (15 s reset, pet every 5 s):

    command = /sbin/slinit-watchdogd -t 15 -i 5

Second WDT on an SoC that exposes multiple watchdog nodes:

    command = /sbin/slinit-watchdogd -d /dev/watchdog1

Graceful stop that disarms cleanly (default *slinitctl stop*
sends SIGTERM):

    slinitctl stop watchdog-pet

Hand-off to an external **watchdogd**(8) at runtime:

    slinitctl signal PWR watchdog-pet
    slinitctl start alt-watchdogd

# EXIT STATUS

**0**
:   Graceful shutdown (*SIGTERM* / *SIGINT* / *SIGPWR*). WDT
    disarmed via magic-close or handed off (armed) via SIGPWR.

**1**
:   Runtime error (open / WDIOC_SETTIMEOUT / initial pet
    failed).

**2**
:   Usage error (bad flag / non-positive timeout).

# FAILURE MODES

- **Missing device**: **open**(2) fails with ENOENT; the
  daemon exits 1 and slinit's respawn supervisor cycles it.
  On a system without a WDT peripheral the operator should
  simply not enable the service; there's no fallback.

- **Petting hiccup**: a transient *ioctl* failure is logged at
  stderr and the loop continues. Multiple consecutive failures
  cost the WDT its reset window; that's the whole point of the
  safety net.

- **Slinit-watchdogd itself hangs**: the WDT fires, kernel
  triggers a reset. This is desired behaviour — nothing to
  recover, only to log after reboot.

# NOTES

- The WDT ioctl numbers (*WDIOC_SETTIMEOUT*, *WDIOC_KEEPALIVE*)
  are stable across every kernel that ships the watchdog API
  (introduced in Linux 2.4; unchanged since).

- **slinit-watchdogd** does NOT walk **/proc/pressure/**, watch
  a supervised process's health, or run filesystem checks. It
  is deliberately a minimal keep-alive daemon. Full watchdog
  frameworks (skarnet **watchdogd**, Debian **watchdogd**) plug
  in via SIGPWR handover when richer semantics are needed.

- Per-service watchdog supervision (systemd's *WatchdogSec=*)
  is a separate feature and lives in **slinit-service**(5), not
  here.

# SEE ALSO

**slinit**(8), **slinit-service**(5), **watchdog**(9),
**watchdogd**(8)

Kernel-side reference: *Documentation/watchdog/watchdog-api.rst*
in the Linux source tree.
