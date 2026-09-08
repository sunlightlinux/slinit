% SLINIT-JOURNALCTL(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-09-08

# NAME

slinit-journalctl - query the slinit journal

# SYNOPSIS

**slinit-journalctl** [*flags*]

# DESCRIPTION

**slinit-journalctl** is slinit's answer to systemd's **journalctl**(1).
It queries slinit's in-process event bus over the control socket for
live and recent events, and can also read persisted journals directly
from disk (both the Phase B binary format and the Phase C JSONL fall-
back). The tool ships full 65-of-65 systemd short-alias parity — a
diff of the systemd flag list against slinit's yields the empty set.

Three query paths, chosen automatically or forced by flags:

- **Live** (default) — connect to */run/slinit.socket*, submit a
  **CmdJournalQuery**, stream **RplyJournalEntry** frames until
  **RplyJournalDone**. Also the path **--follow** takes.
- **File** (**-i** / **--file**) — read one specific journal file
  (binary or JSONL; magic-detected; **.gz** auto-decompressed).
- **Directory** (**-D** / **--directory**) — iterate every
  *\*.jsonl* / *\*.jsonl.gz* / *\*.slj* file under a directory.

The daemon (**slinit-journald**(8)) does NOT need to be running for
the live path — slinit itself owns the ring buffer that
**slinit-journalctl** reads. The daemon exists to persist events
past reboot / rotation / vacuum; it is not on the critical path for
live queries.

# OPTIONS

## Event selection

**-n**, **--lines=**\ *N*
:   Show only the last *N* matching events. **0** = all. The default
    heuristic caps output at 10 for interactive terminals.

**--no-tail**
:   Inverse of the **-n** default — show every match, not just the
    tail slice.

**-u**, **--unit=**\ *NAME*
:   Filter by service unit name. Repeatable — becomes an OR-set.

**-U**, **--user-unit=**\ *NAME*
:   Filter by user-scope unit. Also forces **--user**.

**-t**, **--identifier=**\ *IDENT*
:   Include events whose SYSLOG_IDENTIFIER matches. Repeatable.

**-T**, **--exclude-identifier=**\ *IDENT*
:   Drop events whose SYSLOG_IDENTIFIER matches. Repeatable.

**-g**, **--grep=**\ *PATTERN*
:   Match MESSAGE against an RE2 regex. Case-insensitive by default
    when *PATTERN* is all-lowercase; overridden by **--case-sensitive**.

**--case-sensitive**[=*BOOL*]
:   Override the case heuristic for **-g**.

**-p**, **--priority=**\ *LVL*
:   Keep events at *LVL* or more urgent. Accepts **0..7** or
    **emerg**, **alert**, **crit**, **err**, **warning**, **notice**,
    **info**, **debug**.

**-S**, **--since=**\ *TIME*, **-U**, **--until=**\ *TIME*
:   Restrict to a time range. See **TIME FORMATS** below.

**--facility=**\ *NAME*\|\ *N*
:   Accepted for parity; slinit does not currently record facility.
    A warning is emitted.

**-k**, **--dmesg**
:   Show only kernel (kmsg) events. slinit reads */dev/kmsg* directly
    from boot start.

**-m**, **--merge**
:   Accepted for parity; no-op on slinit's single-source setup.

## Boot navigation

**--list-boots**
:   List every boot ID the journal covers and exit. Walks the on-
    disk journals under */var/log/slinit-journal/* +
    */run/slinit-journal/* in addition to the ring buffer.

**-b**, **--boot** [*ID*]
:   Restrict output to a boot. Empty argument or **0** = current;
    **-N** = *N*-th prior boot; **+N** = *N*-th after the oldest;
    full 32-hex ID also accepted.

**--this-boot**
:   Alias for **--boot=0**.

## Cursor navigation (resume from a known position)

**-c**, **--cursor=**\ *TOKEN*
:   Resume at *TOKEN* (inclusive).

**--after-cursor=**\ *TOKEN*
:   Resume strictly after *TOKEN* (exclusive).

**--cursor-file=**\ *FILE*
:   Load cursor from *FILE* at start, persist updated cursor at end.
    Useful for polling loops.

**--show-cursor**
:   Print a `-- cursor: s=..;b=..` line after output.

## Source override

**-i**, **--file=**\ *PATH*
:   Read a specific journal file (binary or JSONL; magic-detected;
    **.gz** auto-decompress).

**-D**, **--directory=**\ *DIR*
:   Iterate every *\*.jsonl* / *\*.jsonl.gz* / *\*.slj* file under
    *DIR*.

**--root=**\ *PATH*
:   Prefix applied to default filesystem paths (**--directory**,
    **--disk-usage**). Useful for offline analysis of a rescue
    filesystem.

## Follow mode

**-f**, **--follow**
:   Stream new events as they arrive (Ctrl-C to stop). Not
    compatible with **-r**.

**-r**, **--reverse**
:   Print newest first (ignored under **-f**). The reader seeks to
    the tail and walks backward — not a re-sort, so it's actually
    faster than forward for the same *N*.

## Output format

**-o**, **--output=**\ *FMT*
:   Output format:

    - **short** (default) — `Jan 02 15:04:05 host unit[pid]: msg`
    - **short-precise** / **short-iso** / **short-iso-precise** /
      **short-full** — timestamp-flavour variants
    - **short-monotonic** — kernel monotonic clock
    - **short-unix** — Unix epoch
    - **cat** — just the message text
    - **with-unit** — short + explicit UNIT column
    - **json** — one raw JSON object per event (jq-friendly)
    - **json-pretty** — indented JSON
    - **json-sse** — Server-Sent-Events framing
    - **json-seq** — RFC 7464 record-separator framed
    - **verbose** — multi-line dump of every field
    - **export** — systemd export format (KEY=value lines, blank
      between events)

**--output-fields=**\ *A*,*B*,*C*
:   Restrict verbose/export/JSON output to the named field keys.

## Display modifiers

**-W**, **--no-hostname**
:   Drop the hostname column from short outputs.

**--utc**
:   Render timestamps in UTC instead of the local timezone.

**--truncate-newline**
:   Cut MESSAGE at the first newline.

**--no-full**
:   Ellipsize long fields (~256 chars).

**-l**, **--full**
:   Show full fields (default; kept for parity).

**-a**, **--all**
:   Show every field value with no ellipsis.

**-e**, **--pager-end**, **--no-pager**
:   Accepted for parity; slinit never invokes a pager.

**-q**, **--quiet**
:   Suppress info messages (empty file, ring buffer empty, etc.).

## Introspection (short-circuit — do not stream events)

**-F**, **--field=**\ *NAME*
:   Print distinct values seen for *NAME* across events.

**-N**, **--fields**
:   Print the list of known field names.

**--header**
:   Print the journal file / buffer header metadata.

**--disk-usage**
:   Print total bytes across on-disk journals.

**-I**
:   Query only the latest invocation of **-u** *UNIT*. Mutually
    exclusive with **--invocation**.

## Machine target (nspawn integration)

**-M**, **--machine=**\ *CONTAINER*
:   Query *CONTAINER*'s journal via the machine registry. On systems
    without a running **slinit-machinectl** registry entry for
    *CONTAINER* a warning is emitted and the query hits the host
    journal.

## Maintenance (short-circuit — talks to slinit-journald)

**--sync**
:   Force fsync via SIGUSR1 to **slinit-journald**. Falls back to
    walking the journal directory if no daemon is running.

**--rotate**
:   Force rotation via SIGUSR2. Daemon required.

**--vacuum-size=**\ *SIZE*, **--vacuum-files=**\ *N*, **--vacuum-time=**\ *TIME*
:   Prune rotated files until total on-disk ≤ *SIZE* (bytes with
    K/M/G/T suffix), or keep only the most recent *N* rotated files,
    or drop files older than *TIME* (s/m/h/d/w/M/y).

**--pid-file=**\ *PATH*
:   Override the */run/slinit-journald.pid* lookup path used by
    **--sync** / **--rotate**.

**--flush**
:   Migrate volatile /run journal → persistent /var (via admin
    socket). Daemon required.

**--relinquish-var**
:   Close the persistent sink, reopen at volatile — call before
    **umount /var**.

**--smart-relinquish-var**
:   **--relinquish-var** only when */var* is a separate mountpoint.

## Journal namespaces

**--namespace=**\ *NS*
:   Filter events by their Namespace tag. When set, the wire limit
    switches to client-side **-n** so small limits still surface
    matching events past filter drops.

**--list-namespaces**
:   List namespaces detected via */var/log/slinit-journal.\** and
    */run/slinit-journal.\** directories.

## Disk image dissection (root)

**--image=**\ *PATH*
:   Attach the image via **losetup**(8), mount read-only, locate
    the journal directory (*var/log/slinit-journal* or
    *run/slinit-journal*), query it, detach on exit.

**--image-policy=**\ *POLICY*
:   **loose** (default) | **strict** | **full** colon-separated
    systemd form. **strict** refuses LUKS/LVM/verity partitions.

## FSS (Forward-Secure Sealing)

**--setup-keys**
:   Mint a fresh sealing key; save to **--fss-key** path (default
    */etc/slinit/journal-key*). Print the verification token for
    out-of-band sharing.

**--force**
:   Allow **--setup-keys** to overwrite an existing key file. This
    invalidates prior TAG chains — use only when rotating keys.

**--verify**
:   Walk the FSS TAG chain on **--file** (binary only); needs
    **--fss-key**.

**--fss-key=**\ *PATH*
:   FSS key file for **--verify** (default */etc/slinit/journal-key*).

**--verify-key=**\ *TOKEN*
:   Inline verification token (alternative to **--fss-key** file).

**--interval=**\ *DUR*
:   Epoch duration for **--setup-keys** (default 15m). Shorter =
    finer-grained tamper detection, more TAG overhead.

**--synchronize-on-exit**[=*BOOL*]
:   Accepted for parity; slinit always fsyncs on Close, so this is
    a no-op.

## Catalog

**-x**, **--catalog**
:   Augment MESSAGE with the catalog entry text matched on the
    event's MESSAGE_ID field.

**--dump-catalog**
:   Dump every catalog entry as text.

**--list-catalog**
:   List every catalog MESSAGE_ID, sorted.

**--update-catalog**
:   Rescan */usr/share/slinit-catalog* + friends and rebuild the
    compiled cache.

## Invocation tracking

**--invocation=**\ *UUID*
:   Filter events by SLINIT_INVOCATION_ID (a fresh 128-bit ID slinit
    mints at every service start).

**--list-invocations**
:   With **-u** *UNIT*: list every invocation seen for that unit,
    one row per (invocation-id, first-timestamp, last-timestamp).

## Connection

**--socket-path=**\ *P*
:   Override the control socket path.

**--system**, **--user**
:   Force system-mode socket (*/run/slinit.socket*) or user-mode
    socket (*$XDG_RUNTIME_DIR/slinitctl*).

**--version**, **-h**, **--help**
:   Print version / help and exit.

# TIME FORMATS

**--since** and **--until** accept:

- **now** — current wall clock
- **today** | **yesterday** — local midnight-based
- *YYYY-MM-DD* — local midnight
- *"YYYY-MM-DD HH:MM:SS"* — local wall time (quote to preserve the space)
- **RFC 3339** — `2026-07-31T12:00:00Z` or with numeric offset
- **-N**\ **s** / **-N**\ **m** / **-N**\ **h** / **-N**\ **d** —
  *N* seconds / minutes / hours / days ago

# WIRE PATH

Live queries go:

    slinit-journalctl → CmdJournalQuery (opcode 32)
    → /run/slinit.socket
    → RplyJournalEntry* (streamed)
    → RplyJournalDone

Follow mode substitutes **CmdJournalSubscribe** (opcode 33) for the
query, then reads streamed frames until the client disconnects.

# EXIT STATUS

**0**
:   Query completed. May have printed zero events if nothing
    matched.

**1**
:   Runtime error (socket unreachable, file unreadable, FSS
    verification failed, malformed cursor).

**2**
:   Usage error (unknown flag value, incompatible flag combination,
    non-integer where an integer was required).

# EXAMPLES

Live tail:

    slinit-journalctl -f

Last 100 events from unit *nginx*:

    slinit-journalctl -u nginx -n 100

Errors + higher, current boot, JSON:

    slinit-journalctl -b -p err --output=json

Previous boot's kernel messages:

    slinit-journalctl -b -1 -k

Filtered live tail with grep + tag:

    slinit-journalctl -f -u sshd -g 'accepted\|failed'

Verify an on-disk binary journal file:

    slinit-journalctl --file=/var/log/slinit-journal/system.journal \
      --verify --fss-key=/etc/slinit/journal-key

Vacuum rotated files down to 500 MiB:

    slinit-journalctl --vacuum-size=500M

Container journal (via **slinit-machinectl** registry):

    slinit-journalctl -M alpine-demo -f

Query a rescue image:

    slinit-journalctl --image=/mnt/rescue.img --image-policy=strict \
      -b -1 -p err

# FILES

*/run/slinit.socket*
:   Control socket for live queries.

*/var/log/slinit-journal/*
:   Persistent journal directory. Files here are either binary
    (*\*.journal*) or JSONL (*\*.jsonl*, *\*.jsonl.gz* rotated).

*/run/slinit-journal/*
:   Volatile (tmpfs) journal directory used when the persistent one
    is unwritable.

*/etc/slinit/journal-key*
:   Default FSS sealing key.

*/run/slinit-journald.pid*
:   PID file for **--sync** / **--rotate**.

*/run/slinit-journald.ctl*
:   Admin control socket for **--flush** / **--relinquish-var**.

# SEE ALSO

**slinit-journald**(8), **slinit-journal-migrate**(8),
**slinit**(8), **slinit-service**(5), **slinit-machinectl**(8),
**journalctl**(1) (systemd's, for the 65-flag parity project)
