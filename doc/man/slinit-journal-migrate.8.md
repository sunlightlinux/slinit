% SLINIT-JOURNAL-MIGRATE(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-09-08

# NAME

slinit-journal-migrate - convert JSONL slinit journal history to the binary format

# SYNOPSIS

**slinit-journal-migrate** **--from** *DIR* **--to** *DIR* [*flags*]

# DESCRIPTION

**slinit-journal-migrate** reads every *.jsonl* (and *.jsonl.gz*)
file under **--from** and writes the equivalent events into a
Phase-B binary journal under **--to**. The tool is intended as a
one-time upgrade path when moving an existing install from
**slinit-journald --format=jsonl** to the more compact binary
format.

The source directory is not touched — after the migration
completes and *DIR-to* is verified with **slinit-journalctl
--verify**, the operator can archive or delete the JSONL history.

Ordering of events across files is preserved so
**slinit-journalctl** queries against the destination directory
match what the same queries would have seen against the source.

# FLAGS

**-from** *DIR*
:   Source directory holding JSONL (and JSONL.gz) files. Default
    */var/log/slinit-journal*.

**-to** *DIR*
:   Destination directory for the binary journal. Required. Must
    not already contain journal files — this is a one-shot,
    non-idempotent migration.

**-fsync-every** *N*
:   Fsync the destination journal every *N* events (default 128).
    Higher = faster migration, larger data-loss window on crash.

**-dry-run**
:   Print the migration plan (source files → destination file
    breakdown + event totals) without writing anything.

**-version**
:   Print the migrator's version and exit.

# EXIT STATUS

**0**
:   Migration completed. Destination directory holds the full
    history plus a *.journal* file per source file.

**1**
:   Runtime error (unreadable source, non-empty destination,
    corrupted JSONL that could not be parsed).

**2**
:   Usage error (missing **--to**, source and destination are the
    same directory).

# EXAMPLES

Preview a migration:

    slinit-journal-migrate --from /var/log/slinit-journal \
      --to /var/log/slinit-journal.new --dry-run

Migrate:

    slinit-journal-migrate --from /var/log/slinit-journal \
      --to /var/log/slinit-journal.new

Verify the destination and swap it into place:

    slinit-journalctl --directory=/var/log/slinit-journal.new \
      --verify
    mv /var/log/slinit-journal /var/log/slinit-journal.jsonl.bak
    mv /var/log/slinit-journal.new /var/log/slinit-journal
    slinitctl restart slinit-journald

# SEE ALSO

**slinit-journald**(8), **slinit-journalctl**(8), **slinit**(8)
