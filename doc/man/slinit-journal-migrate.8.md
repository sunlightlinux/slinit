% SLINIT-JOURNAL-MIGRATE(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-09-08

# NAME

slinit-journal-migrate - convert JSONL slinit journal history to the binary format

# SYNOPSIS

**slinit-journal-migrate** **--from** *DIR* **--to** *DIR* [*flags*]

# DESCRIPTION

**slinit-journal-migrate** reads every *.jsonl* (and *.jsonl.gz*)
file directly under **--from**, oldest first, and writes their events
to a single new binary journal file under **--to**, named after the
current UTC date (*YYYY-MM-DD.journal*). It is intended as a one-time
upgrade path when moving an existing install from
**slinit-journald --format=jsonl** to the more compact binary format.

Source files are read in filename order, which for the day files
(*YYYY-MM-DD.jsonl* and their rotated *YYYY-MM-DD.N.jsonl[.gz]*) is
date order. The shutdown tails PID 1 writes
(*shutdown-YYYYMMDD-HHMMSS-BOOT.jsonl*) are placed after the day file
of the same date and before the next one. Events are not re-sorted
inside that order: a day file that spans a reboot still lists that
day's shutdown tail after the whole day.

The source directory is not touched; once the destination has been
checked, the operator can archive or delete the JSONL history.

A line that cannot be parsed as an event is skipped and counted, not
treated as an error. So is a line longer than the largest possible
event (512 KiB plus 1 KiB); it is read past without being held in
memory. The closing summary on stderr reports how many events were
written and how many were skipped.

The migration is all or nothing. Events are written to a hidden
temporary file in **--to** (*.YYYY-MM-DD.journal.migrate-\**), which is
fsynced, closed and only then renamed to its final name, after which
the directory is fsynced. A run that fails at any point, including the
final fsync, exits 1 and removes the temporary file, leaving **--to** as
it was; after fixing the cause, simply run it again.

If **--to** already holds *YYYY-MM-DD.journal* for the current day, the
tool refuses to run and exits 1 without changing it. That file is
either an earlier migration, whose events would be written twice, or
the file a running **slinit-journald** is writing into. Point **--to**
at an empty directory, as in the examples below.

The destination file is not sealed with FSS.

# FLAGS

**-from** *DIR*
:   Source directory holding JSONL (and JSONL.gz) files. Default
    */var/log/slinit-journal*. If it contains no such files the tool
    says so and exits 0.

**-to** *DIR*
:   Destination directory for the binary journal, created if missing.
    Required.

**-fsync-every** *N*
:   Fsync the destination journal every *N* events (default 128).
    Higher = faster migration, larger data-loss window on crash.

**-dry-run**
:   List the source files that would be read, on stderr, without
    writing anything.

**-version**
:   Print the migrator's version and exit.

# EXIT STATUS

**0**
:   Migration completed, or there was nothing to migrate.

**1**
:   Runtime error: the source directory or a source file cannot be
    read, the destination cannot be created, written or fsynced, or
    the destination already holds the current day's journal. Nothing
    is left in the destination.

**2**
:   Usage error (missing **--to**, unknown flag).

# EXAMPLES

Preview a migration:

    slinit-journal-migrate --from /var/log/slinit-journal \
      --to /var/log/slinit-journal.new --dry-run

Migrate:

    slinit-journal-migrate --from /var/log/slinit-journal \
      --to /var/log/slinit-journal.new

Spot-check the result, then swap it into place:

    slinit-journalctl --file=/var/log/slinit-journal.new/$(date -u +%F).journal -n 20
    mv /var/log/slinit-journal /var/log/slinit-journal.jsonl.bak
    mv /var/log/slinit-journal.new /var/log/slinit-journal
    slinitctl restart journald

# SEE ALSO

**slinit-journald**(8), **slinit-journalctl**(8), **slinit**(8)
