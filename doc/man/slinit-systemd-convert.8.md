% SLINIT-SYSTEMD-CONVERT(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-09-08

# NAME

slinit-systemd-convert - port a systemd .service unit to a slinit service file

# SYNOPSIS

**slinit-systemd-convert** [*flags*] *unit-file* [*unit-file* ...]

# DESCRIPTION

**slinit-systemd-convert** reads one or more systemd unit files
(**.service** only) and emits equivalent slinit service files.

Only **.service** units are handled. **timer**, **socket**, **path**,
**mount**, and **target** units are systemd-specific abstractions
that map onto different slinit facilities — timer to a cron-alike
service, socket to **socket-listen** on the target service, path to
**start-on-path-\*** directives, mount to **slinit-mount**(8), target
to a **type = internal** aggregate. Convert those by hand.

The converter recognises the [Unit], [Service] and [Install] keys that
have a slinit counterpart, among them **Description**, **Type**
(**simple**/**exec** → **process**, **forking** → **bgprocess**,
**oneshot** → **scripted**), **ExecStart**, **ExecStop**,
**ExecStartPre**, **ExecStartPost**, **Restart**,
**RestartSec**, **User** / **Group** (merged into **run-as**),
**EnvironmentFile**, **WorkingDirectory**, **RootDirectory**,
**PIDFile**, **KillSignal**, **TimeoutSec** / **TimeoutStartSec** /
**TimeoutStopSec**, **Nice**, **OOMScoreAdjust**, **UMask**,
**LimitNOFILE** / **LimitCORE** / **LimitDATA** / **LimitAS**, the
service directories (**RuntimeDirectory** and its siblings),
**Slice**, **Delegate**, the **Condition\*** keys slinit has,
**NoNewPrivileges**, **PrivateTmp**, **ProtectSystem**,
**ProtectHome**, the **ProtectKernel\***, **ProtectClock** and
**ProtectControlGroups** family, **SystemCallFilter**,
**RestrictAddressFamilies** (allow-lists only) and
**RestrictNamespaces** (**yes** / **no** only).

Dependencies: **Requires** becomes **depends-on**; **Wants** and
**After** become **waits-for**; references to **.target** units are
dropped with a note, since slinit has no targets. **Before** cannot be
mapped and is warned about; **WantedBy** / **RequiredBy** produce a
note suggesting **slinitctl enable**.

Not carried over, and reported: inline **Environment=** (slinit takes
an **EnvironmentFile** only), **ExecReload** (slinit's reload is a
signal — see **reload-signal**), **ExecStopPost**, **WatchdogSec**,
**LoadCredential** / **SetCredential** / **ImportCredential**,
**CapabilityBoundingSet** / **AmbientCapabilities**,
**PrivateDevices**, **PrivateNetwork**, **RestrictSUIDSGID**, and any
other key the converter does not know — **KillMode**,
**RemainAfterExit** and **LimitNPROC** among them. Notes and warnings are printed only with
**--verbose**.

Single-input mode writes to standard output. Batch mode
(**--output-dir**) writes one file per input into *DIR*, named
after the input basename with the *.service* (and any trailing *.in*)
suffix stripped.

# FLAGS

**-output-dir** *DIR*
:   Batch mode: write one slinit file per input into *DIR*, which is
    created if missing. Without this flag, output goes to stdout and
    only one unit may be passed at a time.

**-dry-run**
:   With **-output-dir**: print each file that would be written, on
    stderr, without touching the filesystem.

**-verbose**
:   Print per-service conversion notes to stderr — dropped directives,
    dropped target dependencies, and manual follow-ups.

# EXAMPLES

Convert a single unit and inspect the result:

    slinit-systemd-convert /etc/systemd/system/nginx.service > \
        /etc/slinit.d/nginx

Bulk-convert every systemd unit that ships with a package into a
staging area for review:

    mkdir /tmp/staging
    slinit-systemd-convert --output-dir=/tmp/staging \
      --verbose /usr/lib/systemd/system/*.service

Preview a bulk conversion:

    slinit-systemd-convert --output-dir=/tmp/staging --dry-run \
      --verbose /usr/lib/systemd/system/*.service

# EXIT STATUS

**0**
:   All requested conversions completed.

**1**
:   No input, several inputs without **-output-dir**, the output
    directory could not be created, or one or more inputs could not be
    read, parsed or written — a non-**.service** unit included.

**2**
:   Unknown flag.

# SEE ALSO

**slinit-service**(5), **slinit-openrc-convert**(8),
**slinit-runit-convert**(8), **slinit-check**(8),
**slinit-runner**(8), **systemd.service**(5)
