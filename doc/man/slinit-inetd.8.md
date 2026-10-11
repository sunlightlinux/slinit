% SLINIT-INETD(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-10-11

## NAME

slinit-inetd - accept helper that runs one instance of a program per
connection, with the connected socket on stdio (inetd `nowait`)

## SYNOPSIS

**slinit-inetd** [**\--max-concurrent** *N*] [**\--quiet**] **\--**
*PROGRAM* [*ARGS*...]

## DESCRIPTION

**slinit-inetd** implements inetd's `nowait` mode, which is also
launchd's **InetdCompatibility** with *Wait* false and systemd's
**Accept=yes**. It takes the listening sockets **slinit**(8) passed it
through the **LISTEN_FDS** protocol, accepts each connection, and runs
one instance of *PROGRAM* per connection with the **connected** socket
on standard input, standard output and standard error.

It is not normally invoked by hand. Setting **socket-accept** = *yes* in
a service description makes **slinit** prepend it to the service's
command — see **slinit-service**(5).

Without **socket-accept**, **slinit** hands a service its **listener**
and the service accepts for itself. That is inetd's `wait` mode, it is
what **socket-listen** has always done, and it remains the default.

## WHY THIS IS A SEPARATE PROCESS

The accept loop and the per-connection processes deliberately do not
live inside **slinit**. launchd does not put them inside launchd either:
its equivalent loop is a helper, **launchproxy**(8), handed the listener
and run as an ordinary job.

The reason carries more weight here than on Darwin, because **slinit**
may be PID 1. A per-connection lifecycle inside PID 1 would put a
process birth and death on every inbound connection into the same code
that supervises the system, and when PID 1 dies the kernel panics. Here
the fork storm belongs to a supervised service: if this helper dies,
**slinit** restarts it like anything else, and PID 1 is never in the
path.

## OPTIONS

**\--max-concurrent** *N*
:   Maximum number of connection handlers running at once; further
    connections wait for a slot. Default 64.

    **launchproxy**(8) has no such limit, but classic **inetd** does,
    for a reason worth keeping: an unbounded fork per inbound
    connection is a denial of service that costs the whole machine, not
    just the service. Raise it for a genuinely concurrent workload;
    there is no way to disable it.

**\--quiet**
:   Do not log accepted connections. By default each one is logged with
    its peer and local address, as **launchproxy**(8) does.

## ENVIRONMENT

**LISTEN_FDS**
:   Number of inherited listening sockets, starting at file descriptor
    3. Required; the helper exits non-zero without it.

**LISTEN_PID**
:   When set, it must equal this process's PID. The variable survives an
    **exec** into a process that was never given the descriptors, so
    without the check a shell or wrapper between **slinit** and here
    would make the helper treat whatever file descriptor 3 happens to be
    as a listener.

## EXIT STATUS

Non-zero on a configuration or setup error: no **LISTEN_FDS**, a
**LISTEN_PID** belonging to someone else, or an inherited descriptor
that is not a listening stream socket. A datagram socket has no accept
call and is reported as such.

A transient **accept** failure does not end the run: the listener is
still good, and exiting over one refused connection would take the
service down with it.

## NOTES

Each instance's standard input, output and error are all the same
connected socket, so a program that writes diagnostics to standard
error writes them **to the client**. That is inetd behaviour and
long-standing practice for this class of server, but it is worth
knowing before pointing **socket-accept** at something chatty.

A **udp:** listener cannot be used. There is nothing to accept on a
datagram socket; the service should read the socket it is passed
directly.

## SEE ALSO

**slinit**(8), **slinit-service**(5), **slinit-runner**(8),
**slinitctl**(8)
