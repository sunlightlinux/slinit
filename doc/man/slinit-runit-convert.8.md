% SLINIT-RUNIT-CONVERT(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-09-08

# NAME

slinit-runit-convert - port a runit service directory to a slinit service file

# SYNOPSIS

**slinit-runit-convert** [*flags*] *runit-sv-dir* [*runit-sv-dir* ...]

# DESCRIPTION

**slinit-runit-convert** reads one or more runit service directories
(the *./run*, *./finish*, *./conf* set under */etc/sv/*\ *name*) and
emits equivalent slinit service files. The intended workflow is a
one-time port during migration: run once per svdir, review the
generated file, remove the runit svdir.

The converter recognises the runit vocabulary that maps 1:1 onto
slinit directives (**./run** → **command**, **./finish** →
**finish-command**, **./log/run** → **log-processor**,
**./conf** → **env-file**, **down** marker → **manual = yes**,
**./control/\*** → **control-command-\*** map, **chpst -u** →
**run-as**, **chpst -e** → **env-dir**, **chpst -/** → **chroot**).

Single-input mode writes to standard output. Batch mode
(**--output-dir**) writes one file per input into *DIR*, named
after the input svdir's basename.

# FLAGS

**-output-dir** *DIR*
:   Batch mode: write one slinit file per input into *DIR*. Without
    this flag, output goes to stdout and only one svdir may be
    passed at a time.

**-dry-run**
:   Print what would be written without touching the filesystem.

**-enable-map** *slinitctl-command*
:   Check */var/service/*\ *name* symlink for each input; when the
    symlink exists (i.e. runit had it enabled), suggest a **slinitctl
    enable** command on stderr so the operator knows which converted
    services to auto-start. Argument is the exact CLI verb — defaults
    to **slinitctl enable**.

**-verbose**
:   Print per-service conversion notes to stderr — which runit files
    mapped to which slinit directives, and anything that had to be
    dropped.

# EXAMPLES

Convert a single svdir and inspect the result:

    slinit-runit-convert /etc/sv/nginx > /etc/slinit.d/nginx

Bulk-convert every runit svdir into a slinit config directory:

    slinit-runit-convert --output-dir=/etc/slinit.d \
      --verbose /etc/sv/*

Preview a bulk conversion with enable-mapping suggestions:

    slinit-runit-convert --dry-run --verbose \
      --enable-map='slinitctl enable' /etc/sv/*

# EXIT STATUS

**0**
:   All requested conversions completed.

**1**
:   One or more svdirs could not be read (missing *./run*, permission
    denied, unreadable helper file).

**2**
:   Usage error.

# SEE ALSO

**slinit-service**(5), **slinit-openrc-convert**(8),
**slinit-systemd-convert**(8), **slinit-check**(8),
**runsv**(8), **sv**(1)
