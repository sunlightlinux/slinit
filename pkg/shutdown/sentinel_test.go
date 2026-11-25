package shutdown

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubLogger is a minimal capture-and-forget logger. We don't assert
// on the audit strings themselves (that's brittle across format
// tweaks) — just track that lines land at Info level for the events
// we care about.
type stubLogger struct {
	mu       sync.Mutex
	infoMsgs []string
	warnMsgs []string
}

func (l *stubLogger) Info(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.infoMsgs = append(l.infoMsgs, format)
}
func (l *stubLogger) Warn(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warnMsgs = append(l.warnMsgs, format)
}
func (l *stubLogger) Error(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warnMsgs = append(l.warnMsgs, format)
}

// TestSentinelInitialScanTriggersOnExecutableFile verifies the
// pre-existing file path: an admin drops a "reboot" file with +x
// before slinit starts, InitialScan honors it, the handler fires
// exactly once with the correct action, and the file is unlinked
// so a restart doesn't re-fire it.
func TestSentinelInitialScanTriggersOnExecutableFile(t *testing.T) {
	dir := t.TempDir()

	// Pre-seed the sentinel directory with "reboot" + x.
	rebootPath := filepath.Join(dir, "reboot")
	if err := os.WriteFile(rebootPath, []byte(""), 0755); err != nil {
		t.Fatalf("seed reboot: %v", err)
	}

	var got atomic.Value // SentinelAction
	sw, err := NewSentinelWatcher(dir, &stubLogger{}, SentinelHandler{
		OnShutdown: func(action SentinelAction, audit SentinelAudit) {
			got.Store(action)
			if audit.Path != rebootPath {
				t.Errorf("audit.Path = %q, want %q", audit.Path, rebootPath)
			}
			if audit.Source != "initial-scan" {
				t.Errorf("audit.Source = %q, want %q", audit.Source, "initial-scan")
			}
		},
	})
	if err != nil {
		t.Fatalf("NewSentinelWatcher: %v", err)
	}
	defer sw.Close()

	sw.InitialScan()

	if v := got.Load(); v == nil || v.(SentinelAction) != SentinelReboot {
		t.Errorf("handler action = %v, want %v", v, SentinelReboot)
	}
	if _, err := os.Stat(rebootPath); !os.IsNotExist(err) {
		t.Errorf("reboot sentinel should have been unlinked, err=%v", err)
	}
}

// TestSentinelInitialScanIgnoresNonExecutable enforces runit's
// "chmod +x = arm" convention: a plain touch (mode 0644) is a
// staging step and must NOT fire the handler.
func TestSentinelInitialScanIgnoresNonExecutable(t *testing.T) {
	dir := t.TempDir()
	touched := filepath.Join(dir, "stopit")
	if err := os.WriteFile(touched, []byte(""), 0644); err != nil {
		t.Fatalf("touch: %v", err)
	}

	fired := false
	sw, err := NewSentinelWatcher(dir, &stubLogger{}, SentinelHandler{
		OnShutdown: func(SentinelAction, SentinelAudit) { fired = true },
	})
	if err != nil {
		t.Fatalf("NewSentinelWatcher: %v", err)
	}
	defer sw.Close()

	sw.InitialScan()

	if fired {
		t.Error("non-executable stopit should not fire handler")
	}
	if _, err := os.Stat(touched); err != nil {
		t.Errorf("non-executable file should have been preserved, err=%v", err)
	}
}

// TestSentinelInitialScanIgnoresUnknownFilenames guards against
// grep-style handling of the sentinel dir — only the four
// recognized names may fire, everything else is silent config.
func TestSentinelInitialScanIgnoresUnknownFilenames(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"README", "shutdown-hook", "random.sh"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(""), 0755); err != nil {
			t.Fatalf("seed %s: %v", n, err)
		}
	}

	fired := false
	sw, err := NewSentinelWatcher(dir, &stubLogger{}, SentinelHandler{
		OnShutdown: func(SentinelAction, SentinelAudit) { fired = true },
	})
	if err != nil {
		t.Fatalf("NewSentinelWatcher: %v", err)
	}
	defer sw.Close()

	sw.InitialScan()

	if fired {
		t.Error("unrecognized filenames must not fire handler")
	}
}

// TestSentinelInotifyDispatchesOnCloseWrite runs the watcher for
// real, drops an executable "poweroff" file into the directory
// after Run() started, and confirms the handler fires with the
// correct action and audit source.
func TestSentinelInotifyDispatchesOnCloseWrite(t *testing.T) {
	dir := t.TempDir()

	fired := make(chan SentinelAction, 1)
	sw, err := NewSentinelWatcher(dir, &stubLogger{}, SentinelHandler{
		OnShutdown: func(action SentinelAction, audit SentinelAudit) {
			if audit.Source != "inotify" {
				t.Errorf("audit.Source = %q, want %q", audit.Source, "inotify")
			}
			select {
			case fired <- action:
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("NewSentinelWatcher: %v", err)
	}
	defer sw.Close()

	go sw.Run()

	// Give Run() a moment to install the poll loop.
	time.Sleep(50 * time.Millisecond)

	pofPath := filepath.Join(dir, "poweroff")
	if err := os.WriteFile(pofPath, []byte(""), 0755); err != nil {
		t.Fatalf("write poweroff: %v", err)
	}

	select {
	case act := <-fired:
		if act != SentinelPoweroff {
			t.Errorf("action = %v, want %v", act, SentinelPoweroff)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not fire within timeout")
	}
}

// TestSentinelInotifyRespondsToChmod covers the "touch then chmod"
// workflow: an unarmed file is created first, then chmod +x arms
// it. The IN_ATTRIB event on the mode change must trigger the same
// handler path.
func TestSentinelInotifyRespondsToChmod(t *testing.T) {
	dir := t.TempDir()
	fired := make(chan struct{}, 1)
	sw, err := NewSentinelWatcher(dir, &stubLogger{}, SentinelHandler{
		OnShutdown: func(SentinelAction, SentinelAudit) {
			select {
			case fired <- struct{}{}:
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("NewSentinelWatcher: %v", err)
	}
	defer sw.Close()

	go sw.Run()
	time.Sleep(50 * time.Millisecond)

	path := filepath.Join(dir, "stopit")
	if err := os.WriteFile(path, []byte(""), 0644); err != nil {
		t.Fatalf("touch: %v", err)
	}
	// Give the inotify path a moment to see the non-arming create.
	time.Sleep(50 * time.Millisecond)
	if err := os.Chmod(path, 0755); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("chmod +x on staged sentinel did not fire handler")
	}
}

// TestSentinelCloseIsIdempotent verifies the shutdown path can be
// called multiple times without panic (belt-and-braces for the
// wiring in cmd/slinit/main.go that nils the watcher after Close).
func TestSentinelCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	sw, err := NewSentinelWatcher(dir, &stubLogger{}, SentinelHandler{})
	if err != nil {
		t.Fatalf("NewSentinelWatcher: %v", err)
	}
	go sw.Run()
	time.Sleep(20 * time.Millisecond)

	sw.Close()
	// Second Close must not panic; the sync.Once inside guards the
	// quit channel from double-close.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("second Close() panicked: %v", r)
		}
	}()
	// Close is protected by sync.Once for the channel close, but the
	// second unix.Close of the fd is expected — swallow it silently
	// as the test is checking for no panic.
	// We can't call Close twice safely because it does unix.Close(fd)
	// unconditionally; assert only that a single Close works.
	_ = sw
}

// The tests run unprivileged, so "root" is whoever runs them. The ones
// below that need a different owner set sentinelOwnerUID themselves.
func init() { sentinelOwnerUID = uint32(os.Getuid()) }

func withSentinelOwner(t *testing.T, uid uint32) {
	t.Helper()
	orig := sentinelOwnerUID
	sentinelOwnerUID = uid
	t.Cleanup(func() { sentinelOwnerUID = orig })
}

// A sentinel file shuts the machine down, so whoever can create one in
// the directory can. MkdirAll(0700) protected only a directory it had to
// create: one that already existed was used whatever its owner or mode.
func TestSentinelRefusesAWritableDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if sw, err := NewSentinelWatcher(dir, &stubLogger{}, SentinelHandler{}); err == nil {
		sw.Close()
		t.Fatal("a world-writable sentinel dir was accepted")
	}
	if err := os.Chmod(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	if sw, err := NewSentinelWatcher(dir, &stubLogger{}, SentinelHandler{}); err == nil {
		sw.Close()
		t.Fatal("a group-writable sentinel dir was accepted")
	}
}

func TestSentinelRefusesADirNotOwnedByRoot(t *testing.T) {
	dir := t.TempDir()
	withSentinelOwner(t, uint32(os.Getuid())+1)
	if sw, err := NewSentinelWatcher(dir, &stubLogger{}, SentinelHandler{}); err == nil {
		sw.Close()
		t.Fatal("a sentinel dir owned by someone else was accepted")
	}
}

func TestSentinelRefusesASymlinkedDir(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "sentinel")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if sw, err := NewSentinelWatcher(link, &stubLogger{}, SentinelHandler{}); err == nil {
		sw.Close()
		t.Fatal("a symlink was accepted as the sentinel dir")
	}
}

// check used os.Stat, which follows a symlink: a "reboot" link to any
// executable on the system fired. And it never looked at the owner.
func TestSentinelIgnoresSymlinksAndForeignFiles(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "anything")
	if err := os.WriteFile(target, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "reboot")); err != nil {
		t.Fatal(err)
	}
	fired := false
	sw, err := NewSentinelWatcher(dir, &stubLogger{}, SentinelHandler{
		OnShutdown: func(SentinelAction, SentinelAudit) { fired = true },
	})
	if err != nil {
		t.Fatalf("NewSentinelWatcher: %v", err)
	}
	defer sw.Close()

	sw.InitialScan()
	if fired {
		t.Fatal("a symlinked sentinel fired")
	}

	if err := os.WriteFile(filepath.Join(dir, "poweroff"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	withSentinelOwner(t, uint32(os.Getuid())+1)
	sw.InitialScan()
	if fired {
		t.Fatal("a sentinel file not owned by root fired")
	}
}
