% SLINIT-RESOURCE(7) slinit | Sunlight Linux
% Ionut Nechita
% 2026-07-20

## NAME

slinit-resource - OCF Resource Agent for slinit-managed services

## SYNOPSIS

**slinit-resource** {**start**|**stop**|**monitor**|**status**|**meta-data**|**validate-all**}

## DESCRIPTION

**slinit-resource** is an Open Cluster Framework (OCF) Resource Agent
that lets a Pacemaker / Corosync HA cluster drive any **slinit**(8)
service as a cluster resource.

It is a **/bin/sh** script — not a Go binary — because the OCF spec is
shell-shaped (parameters arrive as **OCF_RESKEY_\*** environment
variables, there is no IPC), and cluster operators expect resource
agents to be auditable shell scripts. The agent shells out to
**slinitctl**(8) on every action; it does not start a daemon of its
own.

The agent is intentionally thin. **slinit** is expected to run on every
node where this resource may be hosted (typically as PID 1), the target
service must be defined identically on each node, and Pacemaker decides
which node owns the service at any given moment. The agent only starts,
stops, and probes — it does not load service descriptions or sync
configuration.

## INSTALL PATH

By Pacemaker convention the agent lives under:

    ${OCF_ROOT}/resource.d/slinit/slinit-resource

with **OCF_ROOT** typically */usr/lib/ocf*. Pacemaker discovers the
**slinit** provider automatically once the directory contains at least
one executable agent.

## OCF ACTIONS

**start**
:   Probe the service first; if it is already **STARTED**, return
    **OCF_SUCCESS** without issuing a start, as required by the OCF
    idempotency rule. Otherwise (including **STARTING**) run
    **slinitctl start**, then poll for up to **start_timeout** seconds
    while the service is still **STARTING**. Returns **OCF_SUCCESS**
    once it is **STARTED**, **OCF_ERR_GENERIC** otherwise.

**stop**
:   Probe the service first; return **OCF_SUCCESS** without issuing a
    stop only if it is exactly **STOPPED** or the **slinit** daemon is
    unreachable. Every other state, including **STARTING** and
    **STOPPING**, gets **slinitctl stop \--ignore-unstarted**, followed
    by polling for up to **stop_timeout** seconds. Returns
    **OCF_SUCCESS** once the service is **STOPPED**, **OCF_ERR_GENERIC**
    otherwise.

**monitor**
:   Probe the service via **slinitctl status** and translate its
    *State:* line to OCF return codes. (That output is meant for
    people and its wording may change; the agent depends on the
    *State:* line keeping its shape.)

    | slinit State        | OCF return code      |
    |---------------------|----------------------|
    | STARTED, STARTING   | OCF_SUCCESS (0)      |
    | STOPPED             | OCF_NOT_RUNNING (7)  |
    | STOPPING            | OCF_ERR_GENERIC (1)  |
    | (anything else)     | OCF_ERR_GENERIC (1)  |

    **OCF_NOT_RUNNING** tells Pacemaker the resource is cleanly stopped
    on this node — after a probe it may then start the resource
    elsewhere without stopping it here — so only **STOPPED** maps to
    it. A **STARTING** service may already have running processes and
    is reported active; a **STOPPING** one is reported as failed, so
    Pacemaker recovers it with a **stop**.

    If the **slinit** daemon is unreachable (socket missing, connection
    refused), the agent reports **OCF_NOT_RUNNING** so Pacemaker can
    place the resource elsewhere rather than treating the node as
    failed. Any other **slinitctl status** failure, such as an unknown
    service, is **OCF_ERR_GENERIC**.

**status**
:   Alias for **monitor** (kept for OCF 1.0 compatibility).

**meta-data**
:   Emit OCF 1.0 XML metadata so Pacemaker auto-discovers the agent's
    parameters and supported actions.

**validate-all**
:   Verify that **slinitctl** is on **PATH** and that the requested
    service is declared on this node. Tolerates a daemon that is not
    yet up — Pacemaker validates before bringing slinit online during
    a probe.

## OCF EXIT CODES

The agent uses the standard OCF return codes:

**OCF_SUCCESS**=0
:   Action completed successfully.

**OCF_ERR_GENERIC**=1
:   Generic operation failure.

**OCF_ERR_ARGS**=2
:   Invocation without an action, or with **usage** / **help** (the
    usage text is printed).

**OCF_ERR_UNIMPLEMENTED**=3
:   Action name not recognised.

**OCF_ERR_CONFIGURED**=6
:   The mandatory **service** parameter is empty, or the service is not
    declared on this node.

**OCF_ERR_INSTALLED**=5
:   **slinitctl** is not on **PATH** (and **slinitctl** parameter does
    not point at a valid binary).

**OCF_NOT_RUNNING**=7
:   Service is not running (clean state from Pacemaker's point of
    view — not an error).

## PARAMETERS

The agent accepts the following parameters via Pacemaker's resource
configuration (which sets the corresponding **OCF_RESKEY_\*** env
variables):

**service** (required)
:   Name of the slinit service to control. Must match a service
    description loaded by the slinit daemon on every node where this
    resource may run.

**socket** (default */run/slinit.socket*)
:   Path to the slinit control socket. Override for user-mode managers
    or non-standard layouts.

**slinitctl** (default *slinitctl*)
:   Path to the **slinitctl** binary. Defaults to a **PATH** lookup.

**start_timeout** (default *30*)
:   Seconds **start** keeps polling for **STARTED** after
    **slinitctl start** returns while the service is still
    **STARTING**.

**stop_timeout** (default *30*)
:   Seconds **stop** keeps polling for **STOPPED** after
    **slinitctl stop** returns.

## EXAMPLE

Configure nginx as a single-node-active cluster resource using **crm**:

```
crm configure primitive nginx ocf:slinit:slinit-resource \
    params service=nginx \
    op start    timeout=30s \
    op stop     timeout=30s \
    op monitor  interval=15s timeout=10s
```

The same shape works for **pcs**:

```
pcs resource create nginx ocf:slinit:slinit-resource \
    service=nginx \
    op monitor interval=15s timeout=10s
```

Both tools resolve the agent at *${OCF_ROOT}/resource.d/slinit/slinit-resource*
and read its parameter list from **meta-data**.

## DESIGN NOTES

The agent is read-only with respect to slinit configuration: it does
**not** call **enable**, **disable**, **add-dep**, or **rm-dep**. The
service must be present in a **services-dir** loaded at slinit start.
This avoids the "configuration drift" failure mode where a service
exists on one node but not another.

A **monitor** that sees a transitional state (**STARTING** /
**STOPPING**) returns **OCF_NOT_RUNNING** rather than
**OCF_ERR_GENERIC** — Pacemaker re-probes on its monitor interval, and
treating the transition as a soft "not yet" avoids a fail-over storm
during normal start/stop traffic.

## SEE ALSO

**slinit**(8), **slinitctl**(8), **slinit-service**(5),
**pacemaker**(8), **crm**(8), **pcs**(8)

OCF Resource Agent API spec:
*https://github.com/ClusterLabs/OCF-spec/blob/main/ra/1.0/resource-agent-api.md*

## AUTHORS

Ionut Nechita and contributors. slinit is licensed under Apache 2.0.
