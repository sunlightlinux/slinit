% SLINIT-JOURNALD(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-09-08

# NAME

slinit-journald - persistent journal daemon for slinit

# SYNOPSIS

**slinit-journald** [*flags*]

# DESCRIPTION

**slinit-journald** consumes events from slinit's in-process event
bus and persists them under */var/log/slinit-journal/* for later
retrieval with **slinit-journalctl**(8). It runs as a normal
service under slinit (**type = process**) — nothing on the write
side depends on slinit-journald being alive; the events sit in
slinit's own ring buffer until the daemon subscribes.

Two on-disk formats are supported:

- **binary** (default, Phase B) — SLJRNL01 fixed-size header + 7
  object types (DATA/FIELD/ENTRY/HASH_TABLE/ENTRY_ARRAY/TAG) with
  jenkins lookup3 hashing. Compact, mmap-friendly, forward-secure
  under FSS sealing.
- **jsonl** (Phase C) — one JSON object per line, gzip-compressed
  on rotation. Human-greppable at the cost of size.

Format is chosen at daemon start via **--format** and cannot be
changed for an existing directory without migration; use
**slinit-journal-migrate**(8) to convert JSONL history into the
binary format.

The daemon opens */run/slinit.socket* on startup and replays any
events slinit's ring buffer already holds, so events emitted
before the daemon bound to its events socket are not lost.

# FLAGS

## Storage

**-dir** *DIR*
:   Directory for persistent journal files (default
    */var/log/slinit-journal*). Ignored under **-dry-run**.

**-format** *FORMAT*
:   Storage format: **binary** (Phase B, default) or **jsonl**
    (Phase C, human-grep-friendly). Cannot be mixed inside one
    directory.

**-compress**
:   Gzip-compress rotated JSONL files (default true). No effect
    under **-format=binary** (v1 binary is not compressed).

**-fsync-every** *N*
:   Fsync the journal every *N* events (default 32).

**-max-size** *BYTES*
:   Rotate the current file when it exceeds *BYTES* (default
    128 MiB). Set 0 to disable size-based rotation.

**-max-age** *DURATION*
:   Rotate the current file when it's older than *DURATION*
    (default 24h). Set 0 to disable age-based rotation.

**-vacuum-size** *BYTES*
:   After each rotation, prune the oldest rotated files until the
    directory total is under *BYTES* (default 4 GiB). Set 0 to
    disable.

**-vacuum-files** *N*
:   After each rotation, prune rotated files down to at most *N*
    (default 100). Set 0 to disable.

**-vacuum-age** *DURATION*
:   Prune rotated files older than *DURATION* (default 720h = 30d).
    Set 0 to disable.

## FSS sealing (binary only)

**-fss-key** *FILE*
:   FSS (Forward-Secure Sealing) key file for binary sealing. An
    empty string disables sealing (default). Mint a key with
    **slinit-journalctl --setup-keys**.

**-fss-tag-every** *N*
:   Seal a TAG every *N* entries (default 32). Higher = less TAG
    overhead but coarser tamper detection window. Only meaningful
    with **-format=binary** and **-fss-key** set.

## IPC + lifecycle

**-socket** *PATH*
:   Events socket path to bind. slinit's emitters connect here.
    Default */run/slinit/events.sock*.

**-admin-socket** *PATH*
:   UNIX SOCK_DGRAM admin control socket. Listens for flush /
    relinquish / rotate / sync commands from
    **slinit-journalctl**. Default */run/slinit-journald.ctl*;
    empty string disables.

**-control-socket** *PATH*
:   slinit control socket for backlog replay at startup. Default
    */run/slinit.socket*; empty string disables replay.

**-pid-file** *PATH*
:   Path to write the daemon's PID. Used by **slinit-journalctl
    --sync** / **--rotate** to identify the target. Default
    */run/slinit-journald.pid*; empty string disables.

**-namespace** *NAME*
:   Journal namespace label — systemd **LogNamespace** equivalent.
    When set, the defaults for **-dir**, **-socket**, **-pid-file**,
    **-admin-socket** all gain the *.NAME* suffix (so multiple
    daemons can coexist), and every incoming event is tagged with
    the namespace so **slinit-journalctl --namespace** can filter.

## Diagnostics

**-dry-run**
:   Print received events to stdout instead of persisting.

# EXIT STATUS

**0**
:   Clean shutdown (SIGTERM/SIGINT).

**1**
:   Runtime error (bind failed, directory unwritable, format
    mismatch on an existing directory).

**2**
:   Usage error.

# FILES

*/var/log/slinit-journal/\*.journal*
:   Binary-format journal files (SLJRNL01 magic).

*/var/log/slinit-journal/\*.jsonl* (+ *.gz* rotated)
:   JSONL-format journal files.

*/run/slinit/events.sock*
:   Events socket that slinit's emitters connect to.

*/run/slinit-journald.ctl*
:   Admin control socket (flush/rotate/sync).

*/run/slinit-journald.pid*
:   Daemon PID file.

# EXAMPLES

Standard install — persistent binary journal with FSS sealing:

    slinit-journald \
      --format=binary \
      --fss-key=/etc/slinit/journal-key

Human-grep-friendly JSONL mode (larger on disk, no sealing):

    slinit-journald --format=jsonl --dir=/var/log/slinit-journal

Namespace-isolated instance (useful for containers or multi-tenant
setups):

    slinit-journald --namespace=web --format=binary

Dry-run to a terminal for debugging:

    slinit-journald --dry-run

# SEE ALSO

**slinit-journalctl**(8), **slinit-journal-migrate**(8),
**slinit**(8), **slinit-service**(5)

The on-disk binary format is specified in the source tree at
*pkg/journalbin/README.md*.
