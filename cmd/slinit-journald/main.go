// slinit-journald — persistent daemon that consumes events from
// /run/slinit/events.sock and (in later batches) writes rotated
// JSONL files under /var/log/slinit-journal/.
//
// v1 (3a) ships the skeleton: bind the socket, receive datagrams
// with SO_PASSCRED, stamp trusted metadata (_pid/_uid/_gid + /proc
// snapshot for _comm/_exe/_cmdline), and emit each event as JSONL
// on stdout when --dry-run is set. The default sink at this stage
// is StdoutSink so operators can smoke-test with:
//
//	slinit-journald --dry-run | jq .
//
// Later batches (3b/3c/3d/3e/3f/3g) swap the sink for a file-writing
// implementation with rotation, indexing, and vacuum.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/journald"
)

// version is stamped at build time via `-X main.version=vX.Y.Z`.
var version = "dev"

func main() {
	var (
		sockPath     = flag.String("socket", journal.SocketPath, "events socket path to bind")
		dir          = flag.String("dir", journald.DefaultJournalDir, "directory for persistent JSONL files (ignored under --dry-run)")
		volatileDir  = flag.String("volatile-dir", journald.DefaultVolatileDir, "fallback directory when --dir is not writable ('' disables fallback)")
		fsyncEvery   = flag.Int("fsync-every", journald.DefaultFsyncEvery, "fsync every N events (ignored under --dry-run)")
		maxSize      = flag.Int64("max-size", journald.DefaultMaxSize, "rotate the current jsonl when it exceeds this many bytes (0 = disabled)")
		maxAge       = flag.Duration("max-age", journald.DefaultMaxAge, "rotate the current jsonl when it's older than this duration (0 = disabled)")
		maxFiles     = flag.Int("vacuum-files", journald.DefaultMaxFiles, "prune to at most N rotated files after each rotation (0 = disabled)")
		maxTotalSize = flag.Int64("vacuum-size", journald.DefaultMaxTotalSize, "prune rotated files until total under this many bytes (0 = disabled)")
		vacuumAge    = flag.Duration("vacuum-age", journald.DefaultVacuumMaxAge, "prune rotated files older than this (0 = disabled)")
		compress     = flag.Bool("compress", true, "gzip-compress rotated .jsonl files (still readable via slinit-journalctl --file)")
		dryRun       = flag.Bool("dry-run", false, "print received events to stdout instead of persisting")
		showVersion  = flag.Bool("version", false, "print version and exit")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: slinit-journald [flags]

Consumes events from the slinit journal event bus and (in later
Phase 3 batches) persists them to /var/log/slinit-journal/*.jsonl.

Flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	// InitIDs so the daemon's own emit path (any logs slinit-journald
	// itself sends) get the right boot/machine IDs. Not strictly
	// required for receiving.
	hostname, _ := os.Hostname()
	if err := journal.InitIDs(hostname); err != nil {
		fmt.Fprintf(os.Stderr, "slinit-journald: init IDs: %v (continuing with transient)\n", err)
	}

	// Sink selection: --dry-run always uses stdout so operators can
	// pipe to jq; otherwise the FileSink writes to
	// $dir/YYYY-MM-DD.jsonl (created if missing) with rotation and
	// vacuum wired through the RotatedHook.
	var sink journald.Sink = journald.StdoutSink{}
	if !*dryRun {
		vacOpts := journald.VacuumOptions{
			MaxFiles:     *maxFiles,
			MaxTotalSize: *maxTotalSize,
			MaxAge:       *vacuumAge,
		}
		// Rotation hook order: compress first (so vacuum sees the
		// final .gz name and byte size), then vacuum. Compression
		// runs synchronously with rotation — the alternative
		// (background goroutine) risks a burst of un-compressed
		// files accumulating during heavy write pressure.
		var hooks []func(string, string)
		if *compress {
			hooks = append(hooks, journald.CompressingRotationHook())
		}
		hooks = append(hooks, journald.VacuumingHook(*dir, vacOpts))
		fs, actualDir, degraded := journald.OpenFileSinkWithFallback(*dir, *volatileDir, journald.FileSinkOptions{
			FsyncEvery:  *fsyncEvery,
			MaxSize:     *maxSize,
			MaxAge:      *maxAge,
			RotatedHook: journald.ChainHooks(hooks...),
		})
		if fs == nil {
			fmt.Fprintf(os.Stderr, "slinit-journald: %v\n", degraded)
			os.Exit(1)
		}
		if degraded != nil {
			fmt.Fprintf(os.Stderr,
				"slinit-journald: WARN primary %s unwritable (%v), degraded to volatile %s (tmpfs — no cross-reboot persistence)\n",
				*dir, degraded, actualDir)
		}
		sink = fs
		fmt.Fprintf(os.Stderr,
			"slinit-journald: writing to %s (fsync=%d, rotate=%s|%s, vacuum=%d files|%s|%s)\n",
			fs.CurrentPath(), *fsyncEvery,
			byteSize(*maxSize), *maxAge,
			*maxFiles, byteSize(*maxTotalSize), *vacuumAge)
	}

	recv, err := journald.NewReceiver(*sockPath, sink)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit-journald: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "slinit-journald: listening on %s\n", recv.Path())

	// Cancellation: SIGTERM / SIGINT triggers clean shutdown so the
	// socket file is removed and the sink flushes before we exit.
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	recv.Run(ctx)

	// Block until ctx is cancelled — Run itself returns immediately
	// because it launches the read loop in a goroutine.
	<-ctx.Done()
	if err := recv.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "slinit-journald: stop: %v\n", err)
		os.Exit(1)
	}
	got, dropped := recv.Stats()
	fmt.Fprintf(os.Stderr, "slinit-journald: shutdown clean (received=%d dropped=%d)\n", got, dropped)
}

// byteSize renders a byte count as a short human string. Used in the
// startup banner so operators see "128MiB" instead of the raw
// 134217728. Kept tiny — the daemon doesn't ship a general units
// helper because this is its only user.
func byteSize(n int64) string {
	const (
		KiB = 1 << 10
		MiB = 1 << 20
		GiB = 1 << 30
	)
	switch {
	case n >= GiB:
		return fmt.Sprintf("%dGiB", n/GiB)
	case n >= MiB:
		return fmt.Sprintf("%dMiB", n/MiB)
	case n >= KiB:
		return fmt.Sprintf("%dKiB", n/KiB)
	default:
		return fmt.Sprintf("%dB", n)
	}
}
