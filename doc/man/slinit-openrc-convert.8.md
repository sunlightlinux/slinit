% SLINIT-OPENRC-CONVERT(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-09-08

# NAME

slinit-openrc-convert - port an OpenRC init.d script to a slinit service file

# SYNOPSIS

**slinit-openrc-convert** [*flags*] *init.d-script* [*init.d-script* ...]

# DESCRIPTION

**slinit-openrc-convert** reads one or more OpenRC-style init.d
scripts and emits equivalent slinit service files. The intended
workflow is a one-time port during migration: run once per script,
review the generated *.slinit.d* file, then remove the init.d
script.

The converter recognises the standard OpenRC vocabulary
(**depend**, **command**, **command_args**, **pidfile**, **name**,
**description**, **required_files**, **need**, **use**, **after**,
**before**, **start_pre**, **start_stop_daemon_args**, and the
common **checkpath**/**start-stop-daemon** patterns). Anything the
converter cannot map cleanly is left as a **command =
/usr/sbin/openrc-run** *script* **start** fallback that just
delegates to the original init.d script; a **-wrapper** flag
overrides the wrapper path when a distribution ships something
other than */usr/sbin/openrc-run*.

Single-input mode writes to standard output. Batch mode
(**--output-dir**) writes one file per input into *DIR*, named
after the input basename.

# FLAGS

**-output-dir** *DIR*
:   Batch mode: write one slinit file per input into *DIR*. Without
    this flag, output goes to stdout and only one script may be
    passed at a time.

**-dry-run**
:   Print what would be written without touching the filesystem.
    Useful for review before a bulk migration.

**-enable-map** *slinitctl-command*
:   For scripts symlinked from */etc/runlevels/\**, suggest a
    **slinitctl enable** command on stderr so the operator knows
    which converted services should be auto-started. Argument is
    the exact CLI verb to suggest — defaults to **slinitctl
    enable**.

**-wrapper** *PREFIX*
:   Invocation prefix for the fallback (custom start/stop) path.
    Must invoke the init.d script when appended with *script*
    **start**. Defaults to */usr/sbin/openrc-run*.

**-verbose**
:   Print per-service conversion notes to stderr — which OpenRC
    directives mapped, which were dropped, which required the
    fallback wrapper.

# EXAMPLES

Convert one init.d script and inspect the result:

    slinit-openrc-convert /etc/init.d/nginx > /tmp/nginx.slinit

Bulk-convert an entire */etc/init.d/* directory into a staging area
for review:

    mkdir /tmp/staging
    slinit-openrc-convert --output-dir=/tmp/staging \
      --verbose /etc/init.d/*

Preview a bulk conversion without writing anything:

    slinit-openrc-convert --dry-run --verbose /etc/init.d/*

Convert and also emit enable suggestions for currently-active
runlevel entries:

    slinit-openrc-convert --output-dir=/etc/slinit.d \
      --enable-map='slinitctl enable' /etc/init.d/*

# EXIT STATUS

**0**
:   All requested conversions completed. Fallback-wrapper
    substitutions do not count as errors.

**1**
:   One or more scripts could not be parsed (unreadable file,
    syntactically invalid init.d, unrecognised header).

**2**
:   Usage error.

# SEE ALSO

**slinit-service**(5), **slinit-runit-convert**(8),
**slinit-systemd-convert**(8), **slinit-check**(8),
**rc-service**(8)
