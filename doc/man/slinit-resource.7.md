# slinit-resource 7 "" "" "slinit \- service management system"

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
:   Bring the slinit service up by issuing **slinitctl start**. If the
    service is already **STARTED**, returns **OCF_SUCCESS** without
    contacting the daemon, as required by the OCF idempotency rule.

**stop**
:   Bring the slinit service down by issuing
    **slinitctl stop \--ignore-unstarted**. If the service is already
    **STOPPED**, returns **OCF_SUCCESS** without contacting the daemon.

**monitor**
:   Probe the service via **slinitctl status** and translate the State
    field to OCF return codes:

    | slinit State        | OCF return code      |
    |---------------------|----------------------|
    | STARTED             | OCF_SUCCESS (0)      |
    | STOPPED, STARTING, STOPPING | OCF_NOT_RUNNING (7) |
    | (anything else)     | OCF_ERR_GENERIC (1)  |

    If the **slinit** daemon is unreachable (socket missing, connection
    refused), the agent reports **OCF_NOT_RUNNING** so Pacemaker can
    place the resource elsewhere rather than treating the node as
    failed.

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
:   Invocation without an action.

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
