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
	return g.inner.Handle(evt)
}

func (g *guardedSink) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inner.Close()
}

// Flush = fsync active sink (SIGUSR1 handler → --sync client).
func (g *guardedSink) Flush() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if s, ok := g.inner.(interface{ Flush() error }); ok {
		return s.Flush()
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
			vacOpts := journald.VacuumOptions{
				MaxFiles:     *maxFiles,
				MaxTotalSize: *maxTotalSize,
				MaxAge:       *vacuumAge,
			}
			// factory closure: called at initial open and at each
			// SIGRTMIN swap to build a sink for the requested dir.
			// The hook chain uses the target dir so vacuum after a
			// swap prunes files under the NEW location, not the old.
			jsonlFactory := func(target string) (journald.Sink, string, error) {
				var hooks []func(string, string)
				if *compress {
					hooks = append(hooks, journald.CompressingRotationHook())
				}
				hooks = append(hooks, journald.VacuumingHook(target, vacOpts))
				fs, actualDir, degraded := journald.OpenFileSinkWithFallback(target, *volatileDir, journald.FileSinkOptions{
					FsyncEvery:  *fsyncEvery,
					MaxSize:     *maxSize,
					MaxAge:      *maxAge,
					RotatedHook: journald.ChainHooks(hooks...),
				})
				if fs == nil {
					return nil, "", degraded
				}
				return fs, actualDir, nil
			}
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
			binaryFactory := func(target string) (journald.Sink, string, error) {
				bs, err := journald.NewBinarySink(journald.BinarySinkOptions{
					Dir:         target,
					FsyncEvery:  *fsyncEvery,
					MaxSize:     *maxSize,
					MaxAge:      *maxAge,
					FSSKey:      fssKey,
					TagEvery:    *fssTagEvery,
					BootID:      journal.BootID(),
					MachineID:   journal.MachineID(),
					RotatedHook: journald.VacuumingHook(target, binVacOpts),
				})
				if err != nil {
					return nil, "", err
				}
				return bs, target, nil
			}
			bs, actualDir, err := binaryFactory(*dir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "slinit-journald: binary sink: %v\n", err)
				os.Exit(1)
			}
			guarded = &guardedSink{
				inner:       bs,
				currentDir:  actualDir,
				primaryDir:  *dir,
				volatileDir: *volatileDir,
				namespace:   *namespace,
				factory:     binaryFactory,
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
		if n, err := replayBacklog(*controlSock, sink); err != nil {
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
	// Best-effort unlink any leftover from a previous run.
	_ = os.Remove(path)
	addr, err := net.ResolveUnixAddr("unixgram", path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit-journald: admin socket resolve %s: %v\n", path, err)
		return
	}
	conn, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit-journald: admin socket bind %s: %v\n", path, err)
		return
	}
	defer conn.Close()
	// Loosen perms so a non-root operator running `slinit-journalctl
	// --flush` can send commands. Datagram content is trusted by
	// design — same trust boundary as SIGUSR1/2 via kill().
	_ = os.Chmod(path, 0o666)
	fmt.Fprintf(os.Stderr, "slinit-journald: admin socket listening on %s\n", path)

	buf := make([]byte, 128)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			// Socket closed on shutdown — exit quietly.
			return
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

func replayBacklog(sockPath string, sink journald.Sink) (int, error) {
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

	replayed := 0
	for {
		typ, body, err := control.ReadPacket(conn)
		if err != nil {
			return replayed, fmt.Errorf("read reply: %w", err)
		}
		switch typ {
		case control.RplyJournalEntry:
			evt, err := journal.UnmarshalEvent(body)
			if err != nil {
				continue // one bad entry doesn't sink the whole replay
			}
			if err := sink.Handle(evt); err != nil {
				continue
			}
			replayed++
		case control.RplyJournalDone:
			return replayed, nil
		case control.RplyJournalErr:
			return replayed, fmt.Errorf("server: %s", string(body))
		case control.RplyBadReq:
			return replayed, fmt.Errorf("server rejected CmdJournalQuery (protocol mismatch?)")
		default:
			return replayed, fmt.Errorf("unexpected reply type %d during replay", typ)
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
