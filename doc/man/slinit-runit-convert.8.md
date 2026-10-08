% SLINIT-RUNIT-CONVERT(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-09-08

# NAME

slinit-runit-convert - port a runit service directory to a slinit service file

# SYNOPSIS

**slinit-runit-convert** [*flags*] *runit-sv-dir* [*runit-sv-dir* ...]

# DESCRIPTION

**slinit-runit-convert** reads one or more runit service directories
(*/etc/sv/*\ *name*) and emits equivalent slinit service files. The
intended workflow is a one-time port during migration: run once per
svdir, review the generated file, remove the runit svdir.

Each svdir becomes a **process** service with **restart = yes**,
**restart-delay = 1** and **working-dir** set to the svdir (runsv runs
*./run* from there). The files in the svdir map as follows:

| runit | slinit |
|---|---|
| *run* | **command**: the daemon line of the script, with a bare command name resolved through **PATH** |
| *finish* | **finish-command = /bin/sh** *svdir*/finish |
| *check* | **ready-check-command = /bin/sh** *svdir*/check |
| *down* | **manual = yes** |
| *conf* | **env-file** |
| *log/run* | a companion service *name*-log with **consumer-of** *name*, and **log-type = pipe** on the primary |
| *control/\** | not mapped; each script produces a warning |
| `sv check DEP` in *run* | **waits-for: DEP** |

**chpst** options in the *run* script map where slinit has an
equivalent: **-u** → **run-as**, **-b** → argv[0], **-e** → **env-file**
(with a warning, since runit's directory of files is a different
format), **-d** / **-m** → **rlimit-data**, **-o** → **rlimit-nofile**,
**-c** → **rlimit-core**, **-/** → **chroot**, **-C** → **working-dir**,
**-l** / **-L** → **lock-file**, **-P** → **new-session**, **-0**,
**-1**, **-2**, **-N** → **close-stdin** / **-stdout** / **-stderr**.
**-U**, **-n**, **-A**, **-p**, **-f**, **-r**, **-t**, **-F** and
**-I** are reported as not mapped.

Single-input mode writes to standard output, the companion log
service (if any) following the primary after a separator comment.
Batch mode (**--output-dir**) writes one file per service into *DIR*,
named after the svdir's basename (and *name*-log).

Warnings and notes are printed only with **-verbose**.

# FLAGS

**-output-dir** *DIR*
:   Batch mode: write the slinit files into *DIR*, which is created if
    missing. Without this flag, output goes to stdout and only one
    svdir may be passed at a time.

**-dry-run**
:   With **-output-dir**: print each file that would be written, on
    stderr, without touching the filesystem.

**-enable-map**
:   For each converted svdir whose name has a */var/service/*\ *name*
    symlink (runit had it enabled), print a **slinitctl enable** *name*
    line on stderr at the end, so the operator knows which converted
    services to auto-start.

**-verbose**
:   Print per-service conversion notes to stderr — which runit files
    and **chpst** options could not be mapped, and which dependencies
    were inferred.

# EXAMPLES

Convert a single svdir and inspect the result:

    slinit-runit-convert /etc/sv/nginx > /etc/slinit.d/nginx

Bulk-convert every runit svdir into a slinit config directory:

    slinit-runit-convert --output-dir=/etc/slinit.d \
      --verbose /etc/sv/*

Preview a bulk conversion with enable-mapping suggestions:

    slinit-runit-convert --output-dir=/etc/slinit.d --dry-run \
      --verbose --enable-map /etc/sv/*

# EXIT STATUS

**0**
:   All requested conversions completed.

**1**
:   No input, several inputs without **-output-dir**, the output
    directory could not be created, or one or more svdirs could not be
    read (missing *./run*, permission denied) or written.

**2**
:   Unknown flag.

# SEE ALSO

**slinit-service**(5), **slinit-openrc-convert**(8),
**slinit-systemd-convert**(8), **slinit-check**(8),
**runsv**(8), **sv**(1)
