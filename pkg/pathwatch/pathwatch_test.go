package pathwatch

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// nullLogger drops all output.
type nullLogger struct{}

func (nullLogger) Info(string, ...interface{})  {}
func (nullLogger) Warn(string, ...interface{})  {}
func (nullLogger) Error(string, ...interface{}) {}

// testLogger captures Error messages so tests can inspect them.
type testLogger struct{ t *testing.T }

func (l testLogger) Info(f string, a ...interface{})  { l.t.Logf("INFO: "+f, a...) }
func (l testLogger) Warn(f string, a ...interface{})  { l.t.Logf("WARN: "+f, a...) }
func (l testLogger) Error(f string, a ...interface{}) { l.t.Logf("ERROR: "+f, a...) }

// newWatcher creates a Watcher running in the background and arranges
// Close on test cleanup.
func newWatcher(t *testing.T) *Watcher {
	t.Helper()
	w, err := New(testLogger{t})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go w.Run()
	t.Cleanup(func() { w.Close() })
	return w
}

// waitFire returns true if the counter reaches the expected value within
// timeout. The watcher dispatches events via Run goroutine so callers
// must poll the side-effect.
func waitFire(t *testing.T, fired *atomic.Int32, want int32, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fired.Load() >= want {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func TestAddRejectsRelativePath(t *testing.T) {
	w := newWatcher(t)
	err := w.Add("relative/path", TriggerExists, func() {})
	if err == nil {
		t.Fatal("expected error for relative path")
	}
}

func TestAddRejectsInvalidTrigger(t *testing.T) {
	w := newWatcher(t)
	err := w.Add("/tmp", Trigger(99), func() {})
	if err == nil {
		t.Fatal("expected error for invalid trigger")
	}
}

func TestExistsImmediateFire(t *testing.T) {
	w := newWatcher(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "marker")
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	var fired atomic.Int32
	if err := w.Add(path, TriggerExists, func() { fired.Add(1) }); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if fired.Load() != 1 {
		t.Errorf("expected immediate fire, got %d", fired.Load())
	}
}

func TestExistsFiresOnAppear(t *testing.T) {
	w := newWatcher(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "marker")
	var fired atomic.Int32
	if err := w.Add(path, TriggerExists, func() { fired.Add(1) }); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if fired.Load() != 0 {
		t.Fatalf("fired prematurely: %d", fired.Load())
	}
	if err := os.WriteFile(path, []byte("y"), 0644); err != nil {
		t.Fatal(err)
	}
	if !waitFire(t, &fired, 1, 2*time.Second) {
		t.Errorf("did not fire on appearance, fired=%d", fired.Load())
	}
}

func TestExistsIgnoresOtherFiles(t *testing.T) {
	w := newWatcher(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "want")
	other := filepath.Join(dir, "other")
	var fired atomic.Int32
	if err := w.Add(target, TriggerExists, func() { fired.Add(1) }); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Touching an unrelated sibling must not fire.
	if err := os.WriteFile(other, []byte("z"), 0644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if fired.Load() != 0 {
		t.Errorf("fired on unrelated file: %d", fired.Load())
	}
}

func TestChangedFiresOnCloseWrite(t *testing.T) {
	w := newWatcher(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	var fired atomic.Int32
	if err := w.Add(path, TriggerChanged, func() { fired.Add(1) }); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := os.WriteFile(path, []byte("b"), 0644); err != nil {
		t.Fatal(err)
	}
	if !waitFire(t, &fired, 1, 2*time.Second) {
		t.Errorf("did not fire on close-write, fired=%d", fired.Load())
	}
}

func TestChangedErrorsOnMissingPath(t *testing.T) {
	w := newWatcher(t)
	dir := t.TempDir()
	err := w.Add(filepath.Join(dir, "nope"), TriggerChanged, func() {})
	if err == nil {
		t.Fatal("expected error for missing path with TriggerChanged")
	}
}

func TestModifiedFiresOnWrite(t *testing.T) {
	w := newWatcher(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	if err := os.WriteFile(path, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	var fired atomic.Int32
	if err := w.Add(path, TriggerModified, func() { fired.Add(1) }); err != nil {
		t.Fatalf("Add: %v", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	// IN_MODIFY fires on write before close.
	if !waitFire(t, &fired, 1, 2*time.Second) {
		f.Close()
		t.Errorf("did not fire on write, fired=%d", fired.Load())
		return
	}
	f.Close()
}

func TestDirNotEmptyImmediateWhenPopulated(t *testing.T) {
	w := newWatcher(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	var fired atomic.Int32
	if err := w.Add(dir, TriggerDirNotEmpty, func() { fired.Add(1) }); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if fired.Load() != 1 {
		t.Errorf("expected immediate fire, got %d", fired.Load())
	}
}

func TestDirNotEmptyFiresOnFirstEntry(t *testing.T) {
	w := newWatcher(t)
	dir := t.TempDir()
	var fired atomic.Int32
	if err := w.Add(dir, TriggerDirNotEmpty, func() { fired.Add(1) }); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if fired.Load() != 0 {
		t.Fatalf("fired prematurely: %d", fired.Load())
	}
	if err := os.WriteFile(filepath.Join(dir, "drop"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if !waitFire(t, &fired, 1, 2*time.Second) {
		t.Errorf("did not fire on first entry, fired=%d", fired.Load())
	}
}

func TestDirNotEmptyErrorsOnFile(t *testing.T) {
	w := newWatcher(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	err := w.Add(path, TriggerDirNotEmpty, func() {})
	if err == nil {
		t.Fatal("expected error: TriggerDirNotEmpty on a file")
	}
}

func TestOneShotIgnoresSubsequentEvents(t *testing.T) {
	w := newWatcher(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	var fired atomic.Int32
	if err := w.Add(path, TriggerModified, func() { fired.Add(1) }); err != nil {
		t.Fatalf("Add: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(path, []byte("b"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(300 * time.Millisecond)
	if got := fired.Load(); got != 1 {
		t.Errorf("expected exactly one fire, got %d", got)
	}
}

func TestRearmFiresAgain(t *testing.T) {
	w := newWatcher(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	var fired atomic.Int32
	if err := w.Add(path, TriggerModified, func() { fired.Add(1) }); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := os.WriteFile(path, []byte("b"), 0644); err != nil {
		t.Fatal(err)
	}
	if !waitFire(t, &fired, 1, 2*time.Second) {
		t.Fatalf("did not fire initially")
	}

	if err := w.Rearm(path); err != nil {
		t.Fatalf("Rearm: %v", err)
	}
	if err := os.WriteFile(path, []byte("c"), 0644); err != nil {
		t.Fatal(err)
	}
	if !waitFire(t, &fired, 2, 2*time.Second) {
		t.Errorf("did not fire after rearm, fired=%d", fired.Load())
	}
}

func TestRearmExistsRefiresImmediatelyIfStillThere(t *testing.T) {
	w := newWatcher(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "m")
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	var fired atomic.Int32
	if err := w.Add(path, TriggerExists, func() { fired.Add(1) }); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if fired.Load() != 1 {
		t.Fatalf("first fire expected, got %d", fired.Load())
	}
	if err := w.Rearm(path); err != nil {
		t.Fatalf("Rearm: %v", err)
	}
	if fired.Load() != 2 {
		t.Errorf("expected re-fire on Rearm while path exists, got %d", fired.Load())
	}
}

func TestRemoveStopsFiring(t *testing.T) {
	w := newWatcher(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	var fired atomic.Int32
	if err := w.Add(path, TriggerModified, func() { fired.Add(1) }); err != nil {
		t.Fatalf("Add: %v", err)
	}
	w.Remove(path)
	if err := os.WriteFile(path, []byte("b"), 0644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if fired.Load() != 0 {
		t.Errorf("fired after Remove: %d", fired.Load())
	}
}

func TestDuplicateAddRejected(t *testing.T) {
	w := newWatcher(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := w.Add(path, TriggerModified, func() {}); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if err := w.Add(path, TriggerModified, func() {}); err == nil {
		t.Error("expected error on duplicate Add")
	}
}

func TestTriggerStringForLogs(t *testing.T) {
	cases := []struct {
		tr   Trigger
		want string
	}{
		{TriggerExists, "start-on-path-exists"},
		{TriggerChanged, "start-on-path-changed"},
		{TriggerModified, "start-on-path-modified"},
		{TriggerDirNotEmpty, "start-on-directory-not-empty"},
		{TriggerNone, "none"},
	}
	for _, c := range cases {
		if got := c.tr.String(); got != c.want {
			t.Errorf("%d: got %q want %q", c.tr, got, c.want)
		}
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	w, err := New(nullLogger{})
	if err != nil {
		t.Fatal(err)
	}
	go w.Run()
	w.Close()
	w.Close() // must not panic
}

// TestTwoTriggersInOneDirectory: inotify gives one watch descriptor
// per inode, so two triggers watching the same directory share a wd.
// byWd kept only the last entry, so the first trigger never fired, and
// removing either one dropped the watch the other still needed.
func TestTwoTriggersInOneDirectory(t *testing.T) {
	w := newWatcher(t)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	var firedA, firedB atomic.Int32
	if err := w.Add(a, TriggerExists, func() { firedA.Add(1) }); err != nil {
		t.Fatalf("Add a: %v", err)
	}
	if err := w.Add(b, TriggerExists, func() { firedB.Add(1) }); err != nil {
		t.Fatalf("Add b: %v", err)
	}
	if err := os.WriteFile(a, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if !waitFire(t, &firedA, 1, 2*time.Second) {
		t.Errorf("first trigger in the directory did not fire")
	}

	// Remove b: a's re-armed watch on the same directory must survive.
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	if err := w.Rearm(a); err != nil {
		t.Fatalf("Rearm a: %v", err)
	}
	w.Remove(b)
	if err := os.WriteFile(a, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if !waitFire(t, &firedA, 2, 2*time.Second) {
		t.Errorf("removing one trigger dropped the other's watch")
	}
	if firedB.Load() != 0 {
		t.Errorf("removed trigger fired")
	}
}

// TestTwoTriggersOneDirectoryMasks: a second watch on the same inode
// replaced the first one's mask, so a directory-changed trigger lost
// IN_DELETE once a path-exists trigger shared its directory.
func TestTwoTriggersOneDirectoryMasks(t *testing.T) {
	w := newWatcher(t)
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	var changed, exists atomic.Int32
	if err := w.Add(dir, TriggerChanged, func() { changed.Add(1) }); err != nil {
		t.Fatalf("Add dir: %v", err)
	}
	if err := w.Add(filepath.Join(dir, "later"), TriggerExists, func() { exists.Add(1) }); err != nil {
		t.Fatalf("Add later: %v", err)
	}
	if err := os.Remove(victim); err != nil {
		t.Fatal(err)
	}
	if !waitFire(t, &changed, 1, 2*time.Second) {
		t.Errorf("directory trigger missed IN_DELETE after a second watch on the same inode")
	}
}

// replaceFile writes data to path the way editors and package managers
// do: a temporary file renamed over it.
func replaceFile(t *testing.T, path, data string) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// TestChangedFiresOnRenameOver: the watch sat on the file's inode, so
// a file replaced by rename produced only IN_DELETE_SELF/IN_IGNORED on
// the old inode — no fire, and the IGNORED cleared the watch, so the
// trigger was dead from then on, Rearm or not.
func TestChangedFiresOnRenameOver(t *testing.T) {
	for _, trig := range []Trigger{TriggerChanged, TriggerModified} {
		t.Run(trig.String(), func(t *testing.T) {
			w := newWatcher(t)
			path := filepath.Join(t.TempDir(), "config")
			if err := os.WriteFile(path, []byte("a"), 0644); err != nil {
				t.Fatal(err)
			}
			var fired atomic.Int32
			if err := w.Add(path, trig, func() { fired.Add(1) }); err != nil {
				t.Fatalf("Add: %v", err)
			}
			replaceFile(t, path, "b")
			if !waitFire(t, &fired, 1, 2*time.Second) {
				t.Fatalf("did not fire on rename-over")
			}
			if err := w.Rearm(path); err != nil {
				t.Fatalf("Rearm: %v", err)
			}
			replaceFile(t, path, "c")
			if !waitFire(t, &fired, 2, 2*time.Second) {
				t.Fatalf("did not fire on a second rename-over after Rearm")
			}
			if err := w.Rearm(path); err != nil {
				t.Fatalf("Rearm: %v", err)
			}
			if err := os.WriteFile(path, []byte("d"), 0644); err != nil {
				t.Fatal(err)
			}
			if !waitFire(t, &fired, 3, 2*time.Second) {
				t.Fatalf("did not fire on an in-place write to the replacement")
			}
			// A sibling's changes are not the watched file's.
			if err := w.Rearm(path); err != nil {
				t.Fatalf("Rearm: %v", err)
			}
			if err := os.WriteFile(filepath.Join(filepath.Dir(path), "other"), []byte("x"), 0644); err != nil {
				t.Fatal(err)
			}
			time.Sleep(200 * time.Millisecond)
			if n := fired.Load(); n != 3 {
				t.Errorf("fired on a sibling file: %d", n)
			}
		})
	}
}

// TestFileTriggerIgnoresSiblingWrites: a changed/modified trigger on a
// file watched its directory for writes, so every write by any process
// to any file in that directory (/var/log, /run) woke PID 1's inotify
// loop, only to be filtered out by name. Writes to a sibling must not
// queue an event at all; the watcher is not run, so whatever the kernel
// queued is still there to see. Before the fix the inotify fd was
// readable after the sibling writes, for both triggers.
func TestFileTriggerIgnoresSiblingWrites(t *testing.T) {
	for _, trig := range []Trigger{TriggerChanged, TriggerModified} {
		t.Run(trig.String(), func(t *testing.T) {
			w, err := New(testLogger{t})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			t.Cleanup(func() { unix.Close(w.fd) })
			dir := t.TempDir()
			path, sibling := filepath.Join(dir, "config"), filepath.Join(dir, "busy.log")
			for _, f := range []string{path, sibling} {
				if err := os.WriteFile(f, []byte("a"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Add(path, trig, func() {}); err != nil {
				t.Fatalf("Add: %v", err)
			}
			queued := func() bool {
				fds := []unix.PollFd{{Fd: int32(w.fd), Events: unix.POLLIN}}
				n, _ := unix.Poll(fds, 100)
				return n > 0
			}

			f, err := os.OpenFile(sibling, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 100; i++ {
				f.WriteString("line\n")
			}
			f.Close()
			if queued() {
				t.Errorf("writes to a sibling file queued inotify events")
			}

			// The harness can still see one: a write to the file itself.
			if err := os.WriteFile(path, []byte("b"), 0644); err != nil {
				t.Fatal(err)
			}
			if !queued() {
				t.Errorf("a write to the watched file queued no event")
			}
		})
	}
}

// TestFileTriggerSharesDirectoryWithExists: a changed trigger's
// directory watch shares its wd with a path-exists trigger in the same
// directory; each fires on its own name, and removing the exists
// trigger leaves the changed trigger's directory watch in place.
func TestFileTriggerSharesDirectoryWithExists(t *testing.T) {
	w := newWatcher(t)
	dir := t.TempDir()
	path, later := filepath.Join(dir, "config"), filepath.Join(dir, "later")
	if err := os.WriteFile(path, []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	var changed, exists atomic.Int32
	if err := w.Add(path, TriggerChanged, func() { changed.Add(1) }); err != nil {
		t.Fatalf("Add config: %v", err)
	}
	if err := w.Add(later, TriggerExists, func() { exists.Add(1) }); err != nil {
		t.Fatalf("Add later: %v", err)
	}
	if err := os.WriteFile(later, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if !waitFire(t, &exists, 1, 2*time.Second) {
		t.Errorf("exists trigger sharing the directory did not fire")
	}
	if changed.Load() != 0 {
		t.Errorf("changed trigger fired on another name")
	}
	w.Remove(later)
	replaceFile(t, path, "b")
	if !waitFire(t, &changed, 1, 2*time.Second) {
		t.Errorf("removing the exists trigger dropped the changed trigger's directory watch")
	}
}

// TestCloseWakesRun: Run sleeps in poll with no timeout and Close wakes
// it through an eventfd, then closes the fds once Run has returned. Run
// used to poll every 200ms only to notice Close (five wakeups a second in
// an idle PID 1), and Close closed the inotify fd under it, so Close took
// up to 200ms and Run could poll a reused fd number.
func TestCloseWakesRun(t *testing.T) {
	const n = 10
	var ws []*Watcher
	for i := 0; i < n; i++ {
		w, err := New(nullLogger{})
		if err != nil {
			t.Fatal(err)
		}
		go w.Run()
		ws = append(ws, w)
	}
	// Let every Run reach its poll.
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	for _, w := range ws {
		w.Close()
		select {
		case <-w.done:
		default:
			t.Fatal("Close returned before Run exited")
		}
	}
	// A 200ms poll timeout makes n closes take ~n*100ms on average.
	if d := time.Since(start); d > 150*time.Millisecond {
		t.Errorf("%d Closes took %v; Close did not wake Run", n, d)
	}
}

// TestExistsSurvivesParentRecreate: removing the watched directory makes
// the kernel drop the watch (IN_IGNORED). The entry was left with no
// watch and no log, and never fired again even once the directory and
// the file were back. Run now re-arms it.
func TestExistsSurvivesParentRecreate(t *testing.T) {
	w := newWatcher(t)
	sub := filepath.Join(t.TempDir(), "sub")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	var fired atomic.Int32
	if err := w.Add(filepath.Join(sub, "marker"), TriggerExists, func() { fired.Add(1) }); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := os.Remove(sub); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // let Run see IN_IGNORED
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "marker"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if !waitFire(t, &fired, 1, 3*time.Second) {
		t.Fatal("exists trigger did not fire after its directory was recreated")
	}
}

// TestDirNotEmptySurvivesRecreate: as above for the watched directory
// itself, recreated empty and filled afterwards, so the fire has to come
// from the new watch rather than from the re-arm's own check.
func TestDirNotEmptySurvivesRecreate(t *testing.T) {
	w := newWatcher(t)
	sub := filepath.Join(t.TempDir(), "queue")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	var fired atomic.Int32
	if err := w.Add(sub, TriggerDirNotEmpty, func() { fired.Add(1) }); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := os.Remove(sub); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	// Wait out a re-arm pass, so the entry is armed on the empty dir.
	time.Sleep(2 * time.Second)
	if fired.Load() != 0 {
		t.Fatal("fired on an empty directory")
	}
	if err := os.WriteFile(filepath.Join(sub, "job"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if !waitFire(t, &fired, 1, 2*time.Second) {
		t.Fatal("directory-not-empty did not fire after the directory was recreated")
	}
}

// TestQueueOverflowRearms: IN_Q_OVERFLOW comes with wd -1 and was
// dropped like an event for an unknown watch, so a trigger whose event
// was among those lost never fired. On overflow every unfired entry is
// re-armed, which checks its condition afresh.
func TestQueueOverflowRearms(t *testing.T) {
	// No Run: the file's real IN_CREATE stays unread in the kernel, as
	// if the overflow had swallowed it.
	w, err := New(testLogger{t})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		unix.Close(w.fd)
		unix.Close(w.wakeFd)
	})
	path := filepath.Join(t.TempDir(), "marker")
	var fired atomic.Int32
	if err := w.Add(path, TriggerExists, func() { fired.Add(1) }); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, unix.SizeofInotifyEvent)
	ev := (*unix.InotifyEvent)(unsafe.Pointer(&buf[0]))
	ev.Wd = -1
	ev.Mask = unix.IN_Q_OVERFLOW
	w.dispatch(buf)
	w.rearmLost() // what Run does after each dispatch

	if fired.Load() != 1 {
		t.Fatalf("fired %d times after a queue overflow, want 1", fired.Load())
	}
}

// rearmLoop re-arms path n times, as pathRearmListener does after each
// stop of a service that fails while its condition still holds.
func rearmLoop(t *testing.T, w *Watcher, path string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := w.Rearm(path); err != nil {
			t.Fatalf("Rearm: %v", err)
		}
	}
}

// TestTriggerLimit: each Rearm of a condition that still holds fires
// again at once, so a failing path-activated service was forked by PID 1
// in a tight loop. Like a systemd path unit, an entry stops firing after
// triggerLimitBurst fires within triggerLimitInterval.
func TestTriggerLimit(t *testing.T) {
	w := newWatcher(t)
	path := filepath.Join(t.TempDir(), "marker")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}
	var fired atomic.Int32
	if err := w.Add(path, TriggerExists, func() { fired.Add(1) }); err != nil {
		t.Fatalf("Add: %v", err)
	}
	rearmLoop(t, w, path, 300)
	if got := fired.Load(); got != 200 {
		t.Fatalf("fired %d times in a burst, want the limit of 200", got)
	}
}

// TestTriggerLimitReset: once the limit is hit the entry stays down
// through Rearm until ResetLimit (the service started by other means).
func TestTriggerLimitReset(t *testing.T) {
	w := newWatcher(t)
	path := filepath.Join(t.TempDir(), "marker")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}
	var fired atomic.Int32
	if err := w.Add(path, TriggerExists, func() { fired.Add(1) }); err != nil {
		t.Fatalf("Add: %v", err)
	}
	rearmLoop(t, w, path, 250)
	before := fired.Load()
	rearmLoop(t, w, path, 1)
	if fired.Load() != before {
		t.Fatal("a limited entry fired on Rearm")
	}
	w.ResetLimit(path)
	rearmLoop(t, w, path, 1)
	if fired.Load() != before+1 {
		t.Fatal("Rearm after ResetLimit did not fire")
	}
}
