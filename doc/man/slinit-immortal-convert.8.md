% SLINIT-IMMORTAL-CONVERT(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-10-10

# NAME

slinit-immortal-convert - port an immortal run.yml to a slinit service file

# SYNOPSIS

**slinit-immortal-convert** [*flags*] *run.yml* [*run.yml* ...]

# DESCRIPTION

Converts an **immortal** service definition — the flat YAML file that
**immortal** and **immortaldir** read — into a slinit service file in
the dinit-compatible text format.

immortal supervises one service per process and configures it in a small
file, so nearly every key has a slinit directive behind it. The service
name is taken from the file name without its extension, which is how
**immortaldir** names services too.

Keys that do **not** map are reported on stderr under **-verbose**
rather than dropped. A conversion that silently loses a line is worse
than one that says what it could not do, so every recognised key either
becomes a directive or appears in a note.

## What maps

| immortal | slinit |
|----------|--------|
| `cmd` | **command** (with **type = process**) |
| `cwd` | **working-dir** |
| `user` | **run-as** |
| `wait` | **start-delay** |
| `retries: -1` (default) | **restart = yes** |
| `retries: 0` | **restart = no** |
| `retries: N` | **restart = yes** + **restart-limit-count** = *N* |
| `require` | **assert-service-started** (one per name) |
| `require_cmd` | **exec-condition** |
| `post_exit` | **finish-command** |
| `logger` | **output-logger** |
| `log.file` | **logfile** |
| `log.size` (MegaBytes) | **logfile-max-size** (bytes) |
| `log.num` | **logfile-max-files** |
| `log.age` (seconds) | **logfile-rotate-time** |
| `log.timestamp` | **log-timestamp = iso8601** |
| `stderr.file` | **stderr-logfile** |

`require` becomes a **precondition**, not a dependency. immortal's own
documentation says it plain — "if foo and bar are not running, the
service will not be started" — and immortal never starts them, so
**depends-on** would be the opposite instruction. Add **depends-on** by
hand if slinit should bring them up, and **after** if the services could
be starting at the same moment.

## What does not map

**env**
:   immortal sets variables inline. slinit reads a service's
    environment from a file (**env-file**), a directory (**env-dir**) or
    a command (**env-generator**), never inline. The values are written
    into the output as a comment; move them to a file and add
    **env-file**.

**pid.child**
:   immortal *writes* the supervised process's pid there. slinit's
    **pid-file** is the reverse: a file the daemon writes and slinit
    reads. If the service forks and writes that file itself, use
    **type = bgprocess** with **pid-file**.

**pid.parent**
:   immortal's own supervisor pid. slinit is PID 1 rather than one
    supervisor per service, so there is nothing to write.

**pid.follow**
:   supervising a process slinit did not start has no equivalent. The
    converted service is started and supervised by slinit instead, which
    is a different arrangement — check it is the one wanted.

`stderr` rotation that differs from `log` rotation cannot be honoured:
slinit's **logfile-**\* settings govern both streams, so the `log`
block's values are used for both and a warning says so.

## YAML subset

The reader covers what immortal's configuration uses: top-level
scalars, the `log`, `stderr` and `pid` blocks, the `env` map and the
`require` list. Anchors, aliases, flow sequences, multi-line scalars and
multiple documents are **not** implemented; a line it cannot read is
reported, never guessed at.

This is deliberate. slinit's module carries two third-party packages and
it is PID 1's module, so a general YAML parser is a large surface to add
for one converter.

# FLAGS

**-output-dir** *DIR*
:   Batch mode: write one slinit file per input into *DIR*, named after
    the input file. Without it, the single conversion goes to stdout.

**-dry-run**
:   Print what would be written, to stderr, without touching the
    filesystem. Only meaningful with **-output-dir**.

**-verbose**
:   Print the per-service conversion notes to stderr. Without it the
    conversion is silent about what it could not map, so prefer it on
    a first run.

# EXAMPLES

Convert one service to stdout:

    slinit-immortal-convert /usr/local/etc/immortal/www.yml > /etc/slinit.d/www

Convert an **immortaldir** tree, reading the notes:

    slinit-immortal-convert --verbose --output-dir=/etc/slinit.d \
        /usr/local/etc/immortal/*.yml

See what it would do first:

    slinit-immortal-convert --dry-run --verbose --output-dir=/etc/slinit.d \
        /usr/local/etc/immortal/*.yml

Check the result before trusting it — the converter maps what the two
systems share, and **slinit-check**(8) is what catches the rest:

    slinit-check -d /etc/slinit.d www

# EXIT STATUS

**0**
:   All requested conversions completed.

**1**
:   No input, several inputs without **-output-dir**, the output
    directory could not be created, or one or more inputs could not be
    read, parsed (no `cmd:`) or written.

**2**
:   Unknown flag.

# SEE ALSO

**slinit-service**(5), **slinit-runit-convert**(8),
**slinit-openrc-convert**(8), **slinit-systemd-convert**(8),
**slinit-sysvinit-convert**(8), **slinit-check**(8)
