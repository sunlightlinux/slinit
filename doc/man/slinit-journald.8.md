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

Format is chosen at daemon start via **--format**. The daemon does
not check what an existing directory already holds, so keep one format
per directory; use **slinit-journal-migrate**(8) to convert JSONL
history into the binary format.

The daemon opens */run/slinit.socket* on startup and replays any
events slinit's ring buffer already holds, so events emitted
before the daemon bound to its events socket are not lost.

# FLAGS

## Storage

**-dir** *DIR*
:   Directory for persistent journal files (default
    */var/log/slinit-journal*). Ignored under **-dry-run**.

**-volatile-dir** *DIR*
:   Fallback directory (default */run/slinit-journal*) used when
    **-dir** is not writable at start-up — early boot before */var* is
    mounted read-write, or a read-only root. The start-up fallback
    applies to **-format=jsonl** only; with **-format=binary** an
    unwritable **-dir** is fatal. Set it to the empty string to disable
    the fallback. **slinit-journalctl --flush** moves a volatile journal
    to **-dir** once it is writable.

**-format** *FORMAT*
:   Storage format: **binary** (Phase B, default) or **jsonl**
    (Phase C, human-grep-friendly). Any other value is a usage error.

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
:   UNIX SOCK_DGRAM admin control socket, mode 0666. Accepts the
    commands **flush** (sent by **slinit-journalctl --flush**) and
    **relinquish-var** (sent by **--relinquish-var**, and by
    **--smart-relinquish-var** when */var* is a separate mount);
    **smart-relinquish** is accepted as an alias. Default */run/slinit-journald.ctl*;
    empty string disables.

**-control-socket** *PATH*
:   slinit control socket for backlog replay at startup. Default
    */run/slinit.socket*; empty string disables replay.

**-pid-file** *PATH*
:   Path to write the daemon's PID. **slinit-journalctl --sync** and
    **--rotate** read it to send **SIGUSR1** and **SIGUSR2** (see
    **SIGNALS**). Default */run/slinit-journald.pid*; empty string
    disables.

**-namespace** *NAME*
:   Journal namespace label — systemd **LogNamespace** equivalent.
    When set, the defaults that were not overridden gain a suffix so
    several daemons can coexist: **-dir** and **-volatile-dir** become
    *DIR.NAME*, **-pid-file** */run/slinit-journald.NAME.pid*,
    **-admin-socket** */run/slinit-journald.NAME.ctl*, and **-socket**
    */run/slinit/events-NAME.sock*. Every incoming event is tagged
    with the namespace so **slinit-journalctl --namespace** can filter.

## Diagnostics

**-dry-run**
:   Print received events to stdout instead of persisting.

**-version**
:   Print the version and exit.

# SIGNALS

**SIGTERM**, **SIGINT**
:   Shut down cleanly.

**SIGUSR1**
:   Flush (fsync) the active journal file.

**SIGUSR2**
:   Rotate the active journal file.

# EXIT STATUS

**0**
:   Clean shutdown (SIGTERM/SIGINT).

**1**
:   Runtime error: the events socket cannot be bound, the journal
    directory is unusable, or the FSS key cannot be loaded.

**2**
:   Usage error, including an unknown **-format**.

# FILES

*/var/log/slinit-journal/\*.journal*
:   Binary-format journal files (SLJRNL01 magic).

*/var/log/slinit-journal/\*.jsonl* (+ *.gz* rotated)
:   JSONL-format journal files.

*/run/slinit/events.sock*
:   Events socket that slinit's emitters connect to.

*/run/slinit-journald.ctl*
:   Admin control socket (flush / relinquish).

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

The on-disk binary format is described in the source tree, in the
package documentation of *pkg/journalbin/format.go*.
