package pathwatch

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
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
