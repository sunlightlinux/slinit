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

The converter recognises the [Unit], [Service], and [Install]
section vocabulary that maps onto slinit: **Description**,
**ExecStart**, **ExecStop**, **ExecStartPre**, **ExecStartPost**,
**Type**, **Restart**, **RestartSec**, **User**/**Group**,
**Environment**/**EnvironmentFile**, **WorkingDirectory**,
**PIDFile**, **RemainAfterExit**, **KillMode**, **KillSignal**,
**TimeoutStartSec** / **TimeoutStopSec**, **Nice**,
**OOMScoreAdjust**, **LimitNPROC**/**LimitNOFILE**/etc.,
**Requires** / **Wants** / **After** / **Before**,
**WantedBy** / **RequiredBy**, and the sandbox hardening surface
(**NoNewPrivileges**, **ProtectSystem**, **ProtectHome**,
**PrivateTmp**, **PrivateDevices**, the whole **Protect*** and
**Restrict*** family). Directives with no slinit equivalent are
listed on stderr under **--verbose**.

Single-input mode writes to standard output. Batch mode
(**--output-dir**) writes one file per input into *DIR*, named
after the input basename with the *.service* suffix stripped.

# FLAGS

**-output-dir** *DIR*
:   Batch mode: write one slinit file per input into *DIR*. Without
    this flag, output goes to stdout and only one unit may be passed
    at a time.

**-dry-run**
:   Print what would be written without touching the filesystem.

**-verbose**
:   Print per-service conversion notes to stderr — mapped directives,
    dropped directives, hardening flags that required a **slinit-
    runner** post-fork execve, and any manual-follow-up TODOs.

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

    slinit-systemd-convert --dry-run --verbose \
      /usr/lib/systemd/system/*.service

# EXIT STATUS

**0**
:   All requested conversions completed.

**1**
:   One or more inputs could not be read or parsed.

**2**
:   Usage error, or a non-**.service** unit was passed.

# SEE ALSO

**slinit-service**(5), **slinit-openrc-convert**(8),
**slinit-runit-convert**(8), **slinit-check**(8),
**slinit-runner**(8), **systemd.service**(5)
