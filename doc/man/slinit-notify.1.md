% SLINIT-NOTIFY(1) slinit | Sunlight Linux
% Ionut Nechita
% 2026-11-28

# NAME

slinit-notify - send an sd_notify message to the service manager
(systemd-notify compatible)

# SYNOPSIS

**slinit-notify** [*OPTIONS*] [*VARIABLE*=*VALUE*...]

# DESCRIPTION

A service with **ready-notification = sd-notify** (see
**slinit-service**(5)), which is what a systemd unit with
**Type=notify** becomes, is started when it sends **READY=1** to the
socket named in **$NOTIFY_SOCKET**, and a **watchdog-timeout** is fed by
**WATCHDOG=1**. A daemon linked against libsystemd sends them through
**sd_notify**(3); a shell script runs **slinit-notify**, as it would run
**systemd-notify**(1), which a system without systemd does not have.
It takes the same options for the messages slinit acts on (since 4.1.0).

```
#!/bin/sh
prepare_cache
slinit-notify --ready --status="serving"
while serve_one; do slinit-notify WATCHDOG=1; done
```

# OPTIONS

**--ready**
:   The service is ready (**READY=1**): slinit marks it started.

**--status=***TEXT*
:   A status line (**STATUS=**), which slinit logs.

**--reloading**, **--stopping**
:   **RELOADING=1**, **STOPPING=1**. Sent as given; slinit does not act
    on them yet.

**--pid**[=*PID*]
:   **MAINPID=**: the parent's pid by default, or *PID*, *parent* or
    *self*.

**--booted**
:   Exit 0 if the system runs systemd, which under slinit it does not:
    exit 1. Scripts use it to choose a code path.

**--no-block**
:   Accepted for compatibility; the message is a datagram, sent without
    waiting.

**--version**, **--help**

Any *VARIABLE*=*VALUE* is sent as it is: **WATCHDOG=1** feeds the
watchdog, **ERRNO=**, **EXTEND_TIMEOUT_USEC=** and the like are passed
on. Several options and assignments make one message, one line each.

**$NOTIFY_SOCKET** is a socket path, or an abstract socket written
*@name*.

# EXIT STATUS

0 when the message was sent. 1 when **$NOTIFY_SOCKET** is not set (the
service was not started with sd-notify readiness), the socket cannot be
reached, an argument is invalid, there is nothing to send, or for
**--booted**.

# SEE ALSO

**slinit-service**(5), **slinit**(8), **sd_notify**(3),
**systemd-notify**(1)
