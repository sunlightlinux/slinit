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
review the generated file, then remove the init.d script. A file
that does not start with a **#!** line is rejected.

Script variables are mapped as follows:

| OpenRC | slinit |
|---|---|
| **command**, plus **command_args** / **command_args_foreground** | **command** |
| **command_user** | **run-as** |
| **directory** | **working-dir** |
| **chroot** | **chroot** |
| **pidfile** | **pid-file** |
| **stopsig** | **term-signal** |
| **umask** | **umask** |
| **respawn_delay**, **respawn_max**, **respawn_period** | **restart-delay**, **restart-limit-count**, **restart-limit-interval** |
| **no_new_privs=yes** | **options = no-new-privs** |
| **command_background=yes** | **type = bgprocess** (otherwise **process**) |
| **description**, **name** | comments |
| */etc/conf.d/NAME*, if it exists | **env-file** |

Generated services get **restart = yes**, since OpenRC respawns
supervised daemons. In **depend()**, **need** becomes **depends-on**
and **use** / **after** become **waits-for**; **before**, **provide**
and **keyword** cannot be mapped and produce a warning or note.
Variables such as **required_files**, **required_dirs**,
**start_stop_daemon_args**, **supervise_daemon_args**,
**capabilities**, **output_log** and **extra_commands** are reported
as not mapped.

A script that defines any of the shell functions **start**, **stop**,
**start_pre**, **start_post**, **stop_pre**, **stop_post**,
**restart**, **status** or **reload** cannot be translated. It becomes
a **scripted** service whose **command** is *WRAPPER script* **start**
and whose **stop-command** is *WRAPPER script* **stop**, so the
original init.d script keeps doing the work; **-wrapper** sets
*WRAPPER* (default */usr/sbin/openrc-run*).

Single-input mode writes to standard output. Batch mode
(**--output-dir**) writes one file per input into *DIR*, named
after the input basename.

Warnings and notes about what was not mapped are printed only with
**-verbose**.

# FLAGS

**-output-dir** *DIR*
:   Batch mode: write one slinit file per input into *DIR*, which is
    created if missing. Without this flag, output goes to stdout and
    only one script may be passed at a time.

**-dry-run**
:   With **-output-dir**: print each file that would be written, on
    stderr, without touching the filesystem.

**-enable-map**
:   For each converted script that is linked from
    */etc/runlevels/\*/*, print a **slinitctl enable** *name* line on
    stderr at the end, so the operator knows which converted services
    should be auto-started.

**-wrapper** *PREFIX*
:   Invocation prefix for the fallback (custom start/stop) path.
    Must invoke the init.d script when followed by *script* **start**
    or *script* **stop**. Defaults to */usr/sbin/openrc-run*.

**-verbose**
:   Print per-service conversion notes to stderr — which OpenRC
    directives were dropped, and which required the fallback wrapper.

# EXAMPLES

Convert one init.d script and inspect the result:

    slinit-openrc-convert /etc/init.d/nginx > /tmp/nginx.slinit

Bulk-convert an entire */etc/init.d/* directory into a staging area
for review:

    slinit-openrc-convert --output-dir=/tmp/staging \
      --verbose /etc/init.d/*

Preview a bulk conversion without writing anything:

    slinit-openrc-convert --output-dir=/tmp/staging --dry-run \
      --verbose /etc/init.d/*

Convert and also emit enable suggestions for scripts that are in an
OpenRC runlevel:

    slinit-openrc-convert --output-dir=/etc/slinit.d \
      --enable-map /etc/init.d/*

# EXIT STATUS

**0**
:   All requested conversions completed. Fallback-wrapper
    substitutions do not count as errors.

**1**
:   No input, several inputs without **-output-dir**, the output
    directory could not be created, or one or more scripts could not
    be read, had no **#!** line, or could not be written.

**2**
:   Unknown flag.

# SEE ALSO

**slinit-service**(5), **slinit-runit-convert**(8),
**slinit-systemd-convert**(8), **slinit-check**(8),
**rc-service**(8)
