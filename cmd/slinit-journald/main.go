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
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/journalbin"
	"github.com/sunlightlinux/slinit/pkg/journald"
)

// applyNamespaceDefaults rewrites the still-at-default path flags to
// include the namespace suffix. Explicit operator overrides are
// preserved: we only touch a flag when it still holds the compiled-in
// default. Called once at startup after flag.Parse.
func applyNamespaceDefaults(ns string, sockPath, dir, volatileDir, pidFile, adminSock *string) {
	if *sockPath == journal.SocketPath {
		// journal.SocketPath = "/run/slinit/events.sock"; keep the
		// same parent dir with a per-NS suffix.
		*sockPath = fmt.Sprintf("/run/slinit/events-%s.sock", ns)
	}
	if *dir == journald.DefaultJournalDir {
		*dir = fmt.Sprintf("%s.%s", journald.DefaultJournalDir, ns)
	}
	if *volatileDir == journald.DefaultVolatileDir {
		*volatileDir = fmt.Sprintf("%s.%s", journald.DefaultVolatileDir, ns)
	}
	if *pidFile == "/run/slinit-journald.pid" {
		*pidFile = fmt.Sprintf("/run/slinit-journald.%s.pid", ns)
	}
	if *adminSock == DefaultAdminSocket {
		*adminSock = fmt.Sprintf("/run/slinit-journald.%s.ctl", ns)
	}
}

// DefaultAdminSocket is the ambient control socket slinit-journald
// listens on for out-of-band admin commands (--flush, --relinquish-
// var, --smart-relinquish-var). Kept separate from the events socket
// so a well-known path never conflicts with the operator's --socket
// override. Datagram-oriented (SOCK_DGRAM) because commands are
// single ASCII words with no reply body — send-and-forget matches
// signal semantics without the Go/RT-signal weirdness.
const DefaultAdminSocket = "/run/slinit-journald.ctl"

// guardedSink wraps a Sink with a mutex so signal handlers can Close
// the current inner sink and re-open at a different directory without
// racing the Receiver's Handle loop. Also tracks the current write dir
// + the format-specific factory so --flush (SIGRTMIN+0) and
// --relinquish-var (SIGRTMIN+1) can swap between the persistent primary
// and the volatile tmpfs fallback.
//
// Handle / Close / Flush / Rotate delegate to the inner sink under the
// same lock, so the existing SIGUSR1 / SIGUSR2 (fsync + rotate) paths
// remain safe alongside the new swap operations. The `Flush()` method
// preserves the fsync semantics from Group B; the persistent/volatile
// migration lives on `FlushVolatile()` under a distinct name so the
// two never collide despite systemd's overlapping vocabulary.
type guardedSink struct {
	mu          sync.Mutex
	inner       journald.Sink
	currentDir  string
	primaryDir  string
	volatileDir string
	// namespace, when non-empty, is stamped on every incoming event
	// that doesn't already carry one. Matches systemd's per-namespace
	// journald tagging events with the LogNamespace= value.
	namespace string
	// factory constructs a fresh sink pointing at the given dir.
	// Non-nil second return is the actual directory used (Fallback
	// may pick volatile if primary refuses); third return is an
	// error if construction failed outright.
	factory func(dir string) (journald.Sink, string, error)

	// flushDelay is how long an event can wait for an fsync; zero means
	// journald.DefaultFlushDelay.
	flushDelay time.Duration
	flushOnce  sync.Once
	flushLater *journald.DelayedFlush

	// format is the storage format, and markerPath where the boot and
	// monotonic time of the last durable event are recorded after each
	// fsync and on Close, so PID 1 can persist the rest of a shutdown
	// itself (journald.PersistedMarker). Empty markerPath disables it;
	// only the default namespace writes one.
	format     string
	markerPath string
	lastBoot   string
	lastMts    int64
}

// markerPathFor is the persisted-event marker for a namespace: PID 1
// only knows the default journal, so other namespaces write none.
func markerPathFor(namespace string) string {
	if namespace != "" {
		return ""
	}
	return journald.PersistedMarkerPath
}

// notePersisted records the last event handed to the sink as durable.
// Caller holds g.mu and has just synced the sink.
func (g *guardedSink) notePersisted() {
	if g.markerPath == "" || g.lastBoot == "" {
		return
	}
	if err := journald.WritePersistedMarker(g.markerPath, journald.PersistedMarker{
		BootID: g.lastBoot, Mts: g.lastMts, Dir: g.currentDir, Format: g.format,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "slinit-journald: persisted marker: %v\n", err)
	}
}

func (g *guardedSink) Handle(evt *journal.Event) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	// Stamp namespace so downstream storage + queries can filter by
	// it. Respect the client's own tag if present (allows multiplexed
	// use cases like a stub daemon fan-out).
	if g.namespace != "" && evt.Namespace == "" {
		evt.Namespace = g.namespace
	}
	err := g.inner.Handle(evt)
	if err == nil && evt.BootID != "" {
		g.lastBoot, g.lastMts = evt.BootID, evt.Mts
	}
	g.flushOnce.Do(func() {
		delay := g.flushDelay
		if delay == 0 {
			delay = journald.DefaultFlushDelay
		}
		g.flushLater = journald.NewDelayedFlush(delay, func() {
			if err := g.Flush(); err != nil {
				fmt.Fprintf(os.Stderr, "slinit-journald: flush: %v\n", err)
			}
		})
	})
	g.flushLater.Note()
	return err
}

func (g *guardedSink) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.flushLater != nil {
		g.flushLater.Stop()
	}
	err := g.inner.Close()
	if err == nil {
		g.notePersisted()
	}
	return err
}

// Flush = fsync active sink (SIGUSR1 handler → --sync client).
func (g *guardedSink) Flush() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if s, ok := g.inner.(interface{ Flush() error }); ok {
		if err := s.Flush(); err != nil {
			return err
		}
		g.notePersisted()
	}
	return nil
}

// Rotate = force rotation on active sink (SIGUSR2 handler → --rotate).
func (g *guardedSink) Rotate() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if s, ok := g.inner.(interface{ Rotate() error }); ok {
		return s.Rotate()
	}
	return nil
}

// FlushVolatile migrates journal files from volatile → primary + swaps
// the active sink over. No-op when we're already persistent. Called
// from the SIGRTMIN+0 handler for `slinit-journalctl --flush`.
func (g *guardedSink) FlushVolatile() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.currentDir == g.primaryDir {
		return nil // already persistent
	}
	if err := journald.ProbeWritable(g.primaryDir); err != nil {
		return fmt.Errorf("primary still unwritable: %w", err)
	}
	if err := g.inner.Close(); err != nil {
		return fmt.Errorf("close volatile sink: %w", err)
	}
	if _, err := journald.Migrate(g.volatileDir, g.primaryDir); err != nil {
		// Try to reopen volatile so events keep flowing while the
		// operator debugs the migration failure.
		if s, _, e := g.factory(g.volatileDir); e == nil {
			g.inner = s
			g.currentDir = g.volatileDir
		}
		return fmt.Errorf("migrate: %w", err)
	}
	newSink, actual, err := g.factory(g.primaryDir)
	if err != nil {
		if s, _, e := g.factory(g.volatileDir); e == nil {
			g.inner = s
			g.currentDir = g.volatileDir
		}
		return fmt.Errorf("open primary sink: %w", err)
	}
	g.inner = newSink
	g.currentDir = actual
	return nil
}

// RelinquishVar closes the persistent sink + reopens at volatile.
// Called from SIGRTMIN+1 for `slinit-journalctl --relinquish-var`
// (and its --smart-relinquish-var conditional cousin). Used before
// umount /var so nothing pins the persistent fs.
func (g *guardedSink) RelinquishVar() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.currentDir == g.volatileDir {
		return nil // already volatile
	}
	if err := g.inner.Close(); err != nil {
		return fmt.Errorf("close persistent sink: %w", err)
	}
	newSink, actual, err := g.factory(g.volatileDir)
	if err != nil {
		// Restore persistent so events keep flowing.
		if s, _, e := g.factory(g.primaryDir); e == nil {
			g.inner = s
			g.currentDir = g.primaryDir
		}
		return fmt.Errorf("open volatile sink: %w", err)
	}
	g.inner = newSink
	g.currentDir = actual
	return nil
}

// CurrentDir reports where the active sink is writing. Used by the
// startup banner + tests.
func (g *guardedSink) CurrentDir() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.currentDir
}

// sinkFactory builds the sink for a journal directory from the flags,
// at startup and at each flush / relinquish swap.
type sinkFactory struct {
	volatileDir string
	vacuum      journald.VacuumOptions
	fileOpts    journald.FileSinkOptions
	compress    bool               // jsonl only
	fssKey      *journalbin.FSSKey // binary only
	tagEvery    int                // binary only
}

// vacuumHook prunes dir, the directory the sink was actually opened in;
// the volatile one gets caps scaled to its filesystem, as systemd's
// RuntimeMaxUse= and RuntimeKeepFree= are.
func (f sinkFactory) vacuumHook(dir string) func(string, string) {
	opts := f.vacuum
	if dir == f.volatileDir {
		opts = journald.RuntimeVacuumOptions(dir, opts)
	}
	return journald.VacuumingHook(dir, opts)
}

// jsonl opens a FileSink at target, or at the volatile dir when target
// is unwritable. The third return is nil on a degraded open; the
// caller compares the returned dir with target to tell.
func (f sinkFactory) jsonl(target string) (journald.Sink, string, error) {
	fs, actualDir, degraded := journald.OpenWithFallback(target, f.volatileDir, func(dir string) (*journald.FileSink, error) {
		var hooks []func(string, string)
		if f.compress {
			hooks = append(hooks, journald.CompressingRotationHook())
		}
		hooks = append(hooks, f.vacuumHook(dir))
		opts := f.fileOpts
		opts.RotatedHook = journald.ChainHooks(hooks...)
		return journald.NewFileSinkWithOptions(dir, opts)
	})
	if fs == nil {
		return nil, "", degraded
	}
	return fs, actualDir, nil
}

// binary opens a BinarySink at target, or at the volatile dir when
// target is unwritable, as jsonl does: a read-only or full /var at
// start-up must not stop the journal.
func (f sinkFactory) binary(target string) (journald.Sink, string, error) {
	bs, actualDir, degraded := journald.OpenWithFallback(target, f.volatileDir, func(dir string) (*journald.BinarySink, error) {
		return journald.NewBinarySink(journald.BinarySinkOptions{
			Dir:         dir,
			FsyncEvery:  f.fileOpts.FsyncEvery,
			MaxSize:     f.fileOpts.MaxSize,
			MaxAge:      f.fileOpts.MaxAge,
			FSSKey:      f.fssKey,
			TagEvery:    f.tagEvery,
			BootID:      journal.BootID(),
			MachineID:   journal.MachineID(),
			RotatedHook: f.vacuumHook(dir),
		})
	})
	if bs == nil {
		return nil, "", degraded
	}
	return bs, actualDir, nil
}

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
		controlSock  = flag.String("control-socket", "/run/slinit.socket", "slinit control socket for backlog replay at startup ('' disables replay)")
		compress     = flag.Bool("compress", true, "gzip-compress rotated .jsonl files (jsonl format only; binary not compressed in v1)")
		format       = flag.String("format", "binary", "storage format: binary (default, Phase B) or jsonl (Phase C, human-grep-friendly)")
		fssKeyPath   = flag.String("fss-key", "", "FSS key file for binary sealing ('' disables sealing)")
		fssTagEvery  = flag.Int("fss-tag-every", journalbin.DefaultFSSTagEvery, "seal a TAG every N entries (binary+FSS only)")
		pidFile      = flag.String("pid-file", "/run/slinit-journald.pid", "path to write our PID (for `slinit-journalctl --sync/--rotate`); '' disables")
		adminSock    = flag.String("admin-socket", DefaultAdminSocket, "UNIX SOCK_DGRAM path listening for admin commands (flush/relinquish); '' disables")
		namespace    = flag.String("namespace", "", "journal namespace label (systemd LogNamespace equivalent); when set, defaults for -dir/-volatile-dir/-socket/-pid-file/-admin-socket gain the .NS suffix and incoming events are tagged with this namespace")
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

	// Namespace-aware default path resolution. When --namespace=NS is
	// set and the corresponding path flag still holds its zero-arg
	// default, append `.NS` (or `-NS.sock` for the events socket) so
	// two daemons with different namespaces never fight over the same
	// files. Operators wanting a custom layout can still override each
	// path explicitly; the suffixing only kicks in on the defaults.
	if *namespace != "" {
		applyNamespaceDefaults(*namespace, sockPath, dir, volatileDir, pidFile, adminSock)
	}

	// InitIDs so the daemon's own emit path (any logs slinit-journald
	// itself sends) get the right boot/machine IDs. Not strictly
	// required for receiving.
	hostname, _ := os.Hostname()
	if err := journal.InitIDs(hostname); err != nil {
		fmt.Fprintf(os.Stderr, "slinit-journald: init IDs: %v (continuing with transient)\n", err)
	}

	// Sink selection:
	//   --dry-run  → stdout (JSONL, human-pipeable to jq).
	//   --format=jsonl → Phase C FileSink with gzip rotate + vacuum.
	//   --format=binary (default) → Phase B BinarySink with optional FSS.
	//
	// Both persistent formats share the same rotation + vacuum
	// defaults so operators only tune one policy regardless of
	// --format.
	var sink journald.Sink = journald.StdoutSink{}
	var guarded *guardedSink
	if !*dryRun {
		hostname, _ := os.Hostname()
		_ = hostname
		switch *format {
		case "jsonl":
			// .jsonl.gz: with --compress (the default) every rotated
			// file is gzipped, and a sweep of .jsonl alone pruned none.
			vacOpts := journald.VacuumOptions{
				MaxFiles:     *maxFiles,
				MaxTotalSize: *maxTotalSize,
				MaxAge:       *vacuumAge,
				Suffixes:     []string{".jsonl", ".jsonl.gz"},
			}
			// factory: called at initial open and at each flush /
			// relinquish swap to build a sink for the requested dir.
			jsonlFactory := sinkFactory{
				volatileDir: *volatileDir,
				vacuum:      vacOpts,
				fileOpts: journald.FileSinkOptions{
					FsyncEvery: *fsyncEvery,
					MaxSize:    *maxSize,
					MaxAge:     *maxAge,
				},
				compress: *compress,
			}.jsonl
			fs, actualDir, err := jsonlFactory(*dir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "slinit-journald: %v\n", err)
				os.Exit(1)
			}
			if actualDir != *dir {
				fmt.Fprintf(os.Stderr,
					"slinit-journald: WARN primary %s unwritable, degraded to volatile %s (tmpfs)\n",
					*dir, actualDir)
			}
			guarded = &guardedSink{
				inner:       fs,
				currentDir:  actualDir,
				primaryDir:  *dir,
				volatileDir: *volatileDir,
				namespace:   *namespace,
				factory:     jsonlFactory,
				format:      "jsonl",
				markerPath:  markerPathFor(*namespace),
			}
			sink = guarded
			path := ""
			if p, ok := fs.(interface{ CurrentPath() string }); ok {
				path = p.CurrentPath()
			}
			fmt.Fprintf(os.Stderr,
				"slinit-journald: format=jsonl writing to %s (fsync=%d, rotate=%s|%s, vacuum=%d files|%s|%s)\n",
				path, *fsyncEvery,
				byteSize(*maxSize), *maxAge,
				*maxFiles, byteSize(*maxTotalSize), *vacuumAge)

		case "binary":
			var fssKey *journalbin.FSSKey
			if *fssKeyPath != "" {
				k, err := journalbin.LoadFSSKey(*fssKeyPath)
				if err != nil {
					fmt.Fprintf(os.Stderr, "slinit-journald: FSS key load: %v\n", err)
					os.Exit(1)
				}
				fssKey = k
			}
			binVacOpts := journald.VacuumOptions{
				MaxFiles:     *maxFiles,
				MaxTotalSize: *maxTotalSize,
				MaxAge:       *vacuumAge,
				Suffixes:     []string{".journal"},
			}
			binaryFactory := sinkFactory{
				volatileDir: *volatileDir,
				vacuum:      binVacOpts,
				fileOpts: journald.FileSinkOptions{
					FsyncEvery: *fsyncEvery,
					MaxSize:    *maxSize,
					MaxAge:     *maxAge,
				},
				fssKey:   fssKey,
				tagEvery: *fssTagEvery,
			}.binary
			bs, actualDir, err := binaryFactory(*dir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "slinit-journald: binary sink: %v\n", err)
				os.Exit(1)
			}
			if actualDir != *dir {
				fmt.Fprintf(os.Stderr,
					"slinit-journald: WARN primary %s unwritable, degraded to volatile %s (tmpfs)\n",
					*dir, actualDir)
			}
			guarded = &guardedSink{
				inner:       bs,
				currentDir:  actualDir,
				primaryDir:  *dir,
				volatileDir: *volatileDir,
				namespace:   *namespace,
				factory:     binaryFactory,
				format:      "binary",
				markerPath:  markerPathFor(*namespace),
			}
			sink = guarded
			sealMsg := "unsealed"
			if fssKey != nil {
				sealMsg = fmt.Sprintf("sealed (tag every %d)", *fssTagEvery)
			}
			path := ""
			if p, ok := bs.(interface{ CurrentPath() string }); ok {
				path = p.CurrentPath()
			}
			fmt.Fprintf(os.Stderr,
				"slinit-journald: format=binary writing to %s (fsync=%d, rotate=%s|%s, %s)\n",
				path, *fsyncEvery, byteSize(*maxSize), *maxAge, sealMsg)

		default:
			fmt.Fprintf(os.Stderr, "slinit-journald: unknown --format %q (want jsonl or binary)\n", *format)
			os.Exit(2)
		}
	}

	// Backlog replay: slinit maintains an in-proc ring buffer of every
	// event fired since boot (see pkg/journal.NewEventBuffer wiring in
	// cmd/slinit/main.go). Events emitted BEFORE this daemon starts
	// listening on events.sock have already vanished from the socket
	// path but survive in that buffer. Query them via the control
	// socket and persist through our sink so operator-facing tools see
	// the full boot history.
	//
	// Race window: between backlog-query completion and recv.Run
	// binding events.sock, a live event could be lost. In practice
	// this is <10ms and the ring buffer keeps another copy queryable
	// via slinit-journalctl (no --file). Accepting the tradeoff over
	// the more elaborate seq-based dedup design.
	if *controlSock != "" && !*dryRun {
		var dedup func([]*journal.Event) []*journal.Event
		if guarded != nil && guarded.format == "binary" {
			dedup = dedupAgainstDisk(guarded.currentDir, journal.BootID())
		}
		if n, err := replayBacklog(*controlSock, sink, dedup); err != nil {
			fmt.Fprintf(os.Stderr,
				"slinit-journald: WARN backlog replay from %s failed (%v) — pre-daemon events not persisted\n",
				*controlSock, err)
		} else if n > 0 {
			fmt.Fprintf(os.Stderr, "slinit-journald: replayed %d pre-daemon events from %s\n", n, *controlSock)
		}
	}

	recv, err := journald.NewReceiver(*sockPath, sink)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit-journald: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "slinit-journald: listening on %s\n", recv.Path())

	// PID file: written best-effort so `slinit-journalctl --sync` /
	// `--rotate` can locate us. Removal on shutdown is deferred so a
	// stale PID file after a crash gets cleaned up on the next start
	// via the (implicit) atomic-write in os.WriteFile.
	if *pidFile != "" {
		if err := os.WriteFile(*pidFile, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "slinit-journald: WARN could not write pid-file %s: %v\n", *pidFile, err)
		} else {
			defer os.Remove(*pidFile)
		}
	}

	// Cancellation + on-the-fly maintenance:
	//   SIGINT / SIGTERM → clean shutdown
	//   SIGUSR1          → fsync active sink (`--sync`)
	//   SIGUSR2          → rotate active file (`--rotate`)
	// Volatile ⇄ persistent switching (`--flush`, `--relinquish-var`,
	// `--smart-relinquish-var`) goes through the admin socket instead
	// of signals — Go's os/signal doesn't reliably deliver SIGRTMIN
	// via signal.Notify (uncaught signals in that range terminate the
	// process), so a UNIX DGRAM control socket is both more robust
	// and more extensible for future admin commands.
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh,
		syscall.SIGINT, syscall.SIGTERM,
		syscall.SIGUSR1, syscall.SIGUSR2,
	)
	go func() {
		for sig := range sigCh {
			switch sig {
			case syscall.SIGUSR1:
				if s, ok := sink.(interface{ Flush() error }); ok {
					if err := s.Flush(); err != nil {
						fmt.Fprintf(os.Stderr, "slinit-journald: SIGUSR1 flush: %v\n", err)
					}
				}
			case syscall.SIGUSR2:
				if s, ok := sink.(interface{ Rotate() error }); ok {
					if err := s.Rotate(); err != nil {
						fmt.Fprintf(os.Stderr, "slinit-journald: SIGUSR2 rotate: %v\n", err)
					}
				}
			case syscall.SIGINT, syscall.SIGTERM:
				cancel()
				return
			default:
				fmt.Fprintf(os.Stderr, "slinit-journald: ignoring unexpected signal %v\n", sig)
			}
		}
	}()

	// Admin control socket. Datagram-oriented — each recvfrom is one
	// ASCII command word. Silently ignored when --admin-socket="" or
	// guarded==nil (dry-run: no swappable sink to act on).
	if *adminSock != "" && guarded != nil {
		go runAdminSocket(*adminSock, guarded)
		defer os.Remove(*adminSock)
	}

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

// replayBacklog dials slinit's control socket, sends CmdJournalQuery
// with no filter, and hands every returned event to `sink`. Called
// once at daemon startup so events emitted BEFORE the daemon bound
// events.sock still land on disk. Returns the count of events replayed
// and the first error (control-dial failure, protocol mismatch, sink
// write failure — the last is soft, we count-and-continue).
// runAdminSocket binds path as an abstract-safe UNIX DGRAM socket and
// dispatches each incoming datagram to the guarded sink. Recognised
// commands (single ASCII words, one per datagram):
//
//	flush              → guarded.FlushVolatile()
//	relinquish-var     → guarded.RelinquishVar()
//	smart-relinquish   → guarded.RelinquishVar() (client did the /var
//	                     mountpoint check before dialing)
//
// Unrecognised commands are logged and ignored. The socket is
// removed on daemon exit via the caller's defer.
func runAdminSocket(path string, g *guardedSink) {
	conn, err := listenAdminSocket(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit-journald: %v\n", err)
		return
	}
	defer conn.Close()
	fmt.Fprintf(os.Stderr, "slinit-journald: admin socket listening on %s\n", path)

	buf := make([]byte, 128)
	oob := make([]byte, syscall.CmsgSpace(syscall.SizeofUcred))
	for {
		n, uid, err := readAdminDatagram(conn, buf, oob)
		if err != nil {
			// Socket closed on shutdown — exit quietly.
			return
		}
		if !adminSenderAllowed(uid) {
			fmt.Fprintf(os.Stderr, "slinit-journald: admin: refused command from uid %d\n", uid)
			continue
		}
		cmd := strings.TrimSpace(string(buf[:n]))
		switch cmd {
		case "flush":
			if err := g.FlushVolatile(); err != nil {
				fmt.Fprintf(os.Stderr, "slinit-journald: admin flush: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "slinit-journald: flushed → %s\n", g.CurrentDir())
			}
		case "relinquish-var", "smart-relinquish":
			if err := g.RelinquishVar(); err != nil {
				fmt.Fprintf(os.Stderr, "slinit-journald: admin relinquish: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "slinit-journald: relinquished /var → %s\n", g.CurrentDir())
			}
		default:
			fmt.Fprintf(os.Stderr, "slinit-journald: admin: unknown command %q\n", cmd)
		}
	}
}

// listenAdminSocket binds the admin socket readable and writable by its
// owner only, with SO_PASSCRED on so every command carries its sender's
// credentials. Both flush and relinquish-var move the journal between
// /var and tmpfs, so only root may send them: these are the commands
// journalctl --flush and --relinquish-var send, and systemd-journald
// takes them from root alone.
func listenAdminSocket(path string) (*net.UnixConn, error) {
	// Best-effort unlink any leftover from a previous run.
	_ = os.Remove(path)
	addr, err := net.ResolveUnixAddr("unixgram", path)
	if err != nil {
		return nil, fmt.Errorf("admin socket resolve %s: %w", path, err)
	}
	conn, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		return nil, fmt.Errorf("admin socket bind %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		conn.Close()
		return nil, fmt.Errorf("admin socket chmod %s: %w", path, err)
	}
	rc, err := conn.SyscallConn()
	if err == nil {
		cerr := rc.Control(func(fd uintptr) {
			err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_PASSCRED, 1)
		})
		if cerr != nil {
			err = cerr
		}
	}
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("admin socket SO_PASSCRED %s: %w", path, err)
	}
	return conn, nil
}

// readAdminDatagram reads one command and the uid the kernel stamped on
// it. A datagram without credentials is an error, never a pass.
func readAdminDatagram(conn *net.UnixConn, buf, oob []byte) (int, uint32, error) {
	for {
		n, oobn, _, _, err := conn.ReadMsgUnix(buf, oob)
		if err != nil {
			return 0, 0, err
		}
		msgs, err := syscall.ParseSocketControlMessage(oob[:oobn])
		if err == nil {
			for i := range msgs {
				if cred, err := syscall.ParseUnixCredentials(&msgs[i]); err == nil {
					return n, cred.Uid, nil
				}
			}
		}
		fmt.Fprintln(os.Stderr, "slinit-journald: admin: dropped a command without credentials")
	}
}

// adminSenderAllowed: root, or the uid journald itself runs as (which
// already owns the journal files).
func adminSenderAllowed(uid uint32) bool {
	return uid == 0 || uid == uint32(os.Geteuid())
}

func replayBacklog(sockPath string, sink journald.Sink, dedup func([]*journal.Event) []*journal.Event) (int, error) {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return 0, fmt.Errorf("dial %s: %w", sockPath, err)
	}
	defer conn.Close()

	payload, err := json.Marshal(control.JournalQueryRequest{})
	if err != nil {
		return 0, err
	}
	if err := control.WritePacket(conn, control.CmdJournalQuery, payload); err != nil {
		return 0, fmt.Errorf("send query: %w", err)
	}

	// The whole backlog is read first, so that what an earlier run of
	// this daemon already wrote can be told apart (dedupAgainstDisk).
	// Whatever arrived is persisted even when the transfer breaks off.
	var evts []*journal.Event
	persist := func() int {
		if dedup != nil {
			evts = dedup(evts)
		}
		n := 0
		for _, evt := range evts {
			if sink.Handle(evt) == nil {
				n++
			}
		}
		return n
	}
	for {
		typ, body, err := control.ReadPacket(conn)
		if err != nil {
			return persist(), fmt.Errorf("read reply: %w", err)
		}
		switch typ {
		case control.RplyJournalEntry:
			evt, err := journal.UnmarshalEvent(body)
			if err != nil {
				continue // one bad entry doesn't sink the whole replay
			}
			evts = append(evts, evt)
		case control.RplyJournalDone:
			return persist(), nil
		case control.RplyJournalErr:
			return persist(), fmt.Errorf("server: %s", string(body))
		case control.RplyBadReq:
			return persist(), fmt.Errorf("server rejected CmdJournalQuery (protocol mismatch?)")
		default:
			return persist(), fmt.Errorf("unexpected reply type %d during replay", typ)
		}
	}
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

// dedupAgainstDisk returns the filter that keeps a restarted daemon from
// writing PID 1's backlog a second time: it drops the backlog events of
// this boot that are already in the binary journal in dir.
//
// The backlog is PID 1's whole event ring, and every start replayed all
// of it: a clean restart doubled the journal (442 entries became 884, the
// same events with new sequence numbers), and a daemon that crashes and
// is restarted added up to 4096 duplicates each time.
//
// What is skipped is decided by what is on disk, not by a high-water
// mark: an event that never reached the journal (emitted between the
// backlog query and the socket bind, dropped while the listener was down
// or stalled, refused by the sink) must still be replayed, and only the
// journal itself knows which those are. The files are walked from their
// end while their entries belong to this boot and are not older than the
// oldest backlog event; an entry matches a backlog event with the same
// wall-clock and monotonic time (to the microsecond the format keeps),
// unit and message, each entry standing for one event. Anything that
// cannot be read leaves the backlog whole: a duplicate is better than a
// lost event.
func dedupAgainstDisk(dir, boot string) func([]*journal.Event) []*journal.Event {
	type key struct {
		ts, mts   int64
		unit, msg string
	}
	keyOf := func(e *journal.Event) key { return key{e.Ts / 1000, e.Mts / 1000, e.Unit, e.Msg} }
	return func(ring []*journal.Event) []*journal.Event {
		minMts, ours := int64(0), 0
		for _, e := range ring {
			if e.BootID == boot {
				if ours == 0 || e.Mts < minMts {
					minMts = e.Mts
				}
				ours++
			}
		}
		if ours == 0 || boot == "" {
			return ring
		}
		mr, err := journalbin.OpenDir(dir)
		if err != nil {
			return ring
		}
		defer mr.Close()
		have := map[key]int{}
		for _, r := range mr.Readers() {
			offs, err := r.EntryOffsets()
			if err != nil {
				return ring
			}
			for i := len(offs) - 1; i >= 0; i-- {
				e, err := r.ReadEntryAt(offs[i])
				if err != nil {
					return ring
				}
				// Appends are in arrival order: past an earlier boot, or
				// before the oldest backlog event, nothing can match.
				if e.BootID != boot || e.Mts/1000 < minMts/1000 {
					break
				}
				have[keyOf(e)]++
			}
		}
		out := make([]*journal.Event, 0, len(ring))
		for _, e := range ring {
			if e.BootID == boot {
				if k := keyOf(e); have[k] > 0 {
					have[k]--
					continue
				}
			}
			out = append(out, e)
		}
		return out
	}
}
