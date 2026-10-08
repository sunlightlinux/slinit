% SLINIT-JOURNAL-MIGRATE(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-09-08

# NAME

slinit-journal-migrate - convert JSONL slinit journal history to the binary format

# SYNOPSIS

**slinit-journal-migrate** **--from** *DIR* **--to** *DIR* [*flags*]

# DESCRIPTION

**slinit-journal-migrate** reads every *.jsonl* (and *.jsonl.gz*)
file directly under **--from**, in filename order, and appends their
events to a single binary journal file under **--to**, named after the
current UTC date (*YYYY-MM-DD.journal*). It is intended as a one-time
upgrade path when moving an existing install from
**slinit-journald --format=jsonl** to the more compact binary format.

The source directory is not touched; once the destination has been
checked, the operator can archive or delete the JSONL history.

A line that cannot be parsed as an event is skipped and counted, not
treated as an error. The closing summary on stderr reports how many
events were written and how many were skipped.

The destination file is not sealed with FSS, and the tool does not
check whether **--to** already holds journal files. Running it twice on
the same day appends to the same file, so every event is written twice.

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
    read, or the destination cannot be created or written.

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
    slinitctl restart slinit-journald

# SEE ALSO

**slinit-journald**(8), **slinit-journalctl**(8), **slinit**(8)
