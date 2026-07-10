package svcdirwatch

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testLogger captures log lines for assertions (unused so far, but
// handy for debugging when a test misbehaves).
type testLogger struct{}

func (testLogger) Info(f string, a ...interface{})  {}
func (testLogger) Warn(f string, a ...interface{})  {}
func (testLogger) Error(f string, a ...interface{}) {}

// recorder captures Appeared/Disappeared/Modified callbacks in order.
type recorder struct {
	mu     sync.Mutex
	events []string // "appeared:name" / "disappeared:name" / "modified:name"
	wg     sync.WaitGroup
	want   int32 // events remaining for wg
}

func (r *recorder) handler() Handler {
	return Handler{
		Appeared: func(name string) {
			r.mu.Lock()
			r.events = append(r.events, "appeared:"+name)
			r.mu.Unlock()
			r.done()
		},
		Disappeared: func(name string) {
			r.mu.Lock()
			r.events = append(r.events, "disappeared:"+name)
			r.mu.Unlock()
			r.done()
		},
		Modified: func(name string) {
			r.mu.Lock()
			r.events = append(r.events, "modified:"+name)
			r.mu.Unlock()
			r.done()
		},
	}
}

func (r *recorder) expect(n int) {
	atomic.StoreInt32(&r.want, int32(n))
	r.wg.Add(n)
}

func (r *recorder) done() {
	// Guard against extra events after expected count fills.
	if atomic.AddInt32(&r.want, -1) < 0 {
		return
	}
	r.wg.Done()
}

func (r *recorder) wait(t *testing.T, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		r.mu.Lock()
		got := append([]string{}, r.events...)
		r.mu.Unlock()
		t.Fatalf("timeout waiting for events, got so far: %v", got)
	}
}

func (r *recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.events...)
}

// newTestWatcher returns a watcher with a short debounce interval for
// faster tests.
func newTestWatcher(t *testing.T, h Handler) *Watcher {
	t.Helper()
	w, err := New(testLogger{}, h, Options{DebounceInterval: 40 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go w.Run()
	t.Cleanup(w.Close)
	return w
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestAppearedOnCreate(t *testing.T) {
	dir := t.TempDir()
	var r recorder
	w := newTestWatcher(t, r.handler())
	if err := w.AddDir(dir); err != nil {
		t.Fatalf("AddDir: %v", err)
	}
	r.expect(1)
	writeFile(t, filepath.Join(dir, "svc-a"), "type = process\ncommand = /bin/true\n")
	r.wait(t, 2*time.Second)
	got := r.snapshot()
	if len(got) != 1 || got[0] != "appeared:svc-a" {
		t.Fatalf("want [appeared:svc-a], got %v", got)
	}
}

func TestDisappearedOnDelete(t *testing.T) {
	dir := t.TempDir()
	// Preseed a file before AddDir so it's in the known-set (not appeared).
	writeFile(t, filepath.Join(dir, "svc-b"), "type = process\ncommand = /bin/true\n")
	var r recorder
	w := newTestWatcher(t, r.handler())
	if err := w.AddDir(dir); err != nil {
		t.Fatalf("AddDir: %v", err)
	}
	r.expect(1)
	if err := os.Remove(filepath.Join(dir, "svc-b")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	r.wait(t, 2*time.Second)
	got := r.snapshot()
	if len(got) != 1 || got[0] != "disappeared:svc-b" {
		t.Fatalf("want [disappeared:svc-b], got %v", got)
	}
}

func TestModifiedOnRewrite(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "svc-c"), "type = process\ncommand = /bin/true\n")
	var r recorder
	w := newTestWatcher(t, r.handler())
	if err := w.AddDir(dir); err != nil {
		t.Fatalf("AddDir: %v", err)
	}
	r.expect(1)
	writeFile(t, filepath.Join(dir, "svc-c"), "type = process\ncommand = /bin/false\n")
	r.wait(t, 2*time.Second)
	got := r.snapshot()
	if len(got) != 1 || got[0] != "modified:svc-c" {
		t.Fatalf("want [modified:svc-c], got %v", got)
	}
}

func TestMovedInIsAppeared(t *testing.T) {
	dir := t.TempDir()
	staging := t.TempDir()
	src := filepath.Join(staging, "svc-d")
	writeFile(t, src, "type = process\ncommand = /bin/true\n")
	var r recorder
	w := newTestWatcher(t, r.handler())
	if err := w.AddDir(dir); err != nil {
		t.Fatalf("AddDir: %v", err)
	}
	r.expect(1)
	if err := os.Rename(src, filepath.Join(dir, "svc-d")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	r.wait(t, 2*time.Second)
	got := r.snapshot()
	if len(got) != 1 || got[0] != "appeared:svc-d" {
		t.Fatalf("want [appeared:svc-d], got %v", got)
	}
}

func TestDotfileIgnored(t *testing.T) {
	dir := t.TempDir()
	var r recorder
	w := newTestWatcher(t, r.handler())
	if err := w.AddDir(dir); err != nil {
		t.Fatalf("AddDir: %v", err)
	}
	// No expect() — we assert nothing fires.
	writeFile(t, filepath.Join(dir, ".hidden"), "x")
	writeFile(t, filepath.Join(dir, "editor.swp"), "x")
	writeFile(t, filepath.Join(dir, "backup~"), "x")
	writeFile(t, filepath.Join(dir, "svc.new"), "x")
	writeFile(t, filepath.Join(dir, "svc.bak"), "x")
	writeFile(t, filepath.Join(dir, "svc.d"), "x") // overlay-suffix filter
	time.Sleep(200 * time.Millisecond)
	if got := r.snapshot(); len(got) != 0 {
		t.Fatalf("want no events for junk files, got %v", got)
	}
}

func TestSubdirectoryIgnored(t *testing.T) {
	dir := t.TempDir()
	var r recorder
	w := newTestWatcher(t, r.handler())
	if err := w.AddDir(dir); err != nil {
		t.Fatalf("AddDir: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "svc-e.d"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "svc-e-plain"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := r.snapshot(); len(got) != 0 {
		t.Fatalf("want no events for directories, got %v", got)
	}
}

func TestDebounceCoalesces(t *testing.T) {
	dir := t.TempDir()
	var r recorder
	// Long debounce so we can fire multiple writes before it drains.
	w, err := New(testLogger{}, r.handler(), Options{DebounceInterval: 250 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go w.Run()
	t.Cleanup(w.Close)

	if err := w.AddDir(dir); err != nil {
		t.Fatalf("AddDir: %v", err)
	}
	r.expect(1)
	path := filepath.Join(dir, "svc-f")
	for i := 0; i < 5; i++ {
		writeFile(t, path, fmt.Sprintf("iter=%d\n", i))
		time.Sleep(30 * time.Millisecond) // shorter than debounce
	}
	r.wait(t, 2*time.Second)
	got := r.snapshot()
	if len(got) != 1 || got[0] != "appeared:svc-f" {
		t.Fatalf("want exactly 1 appeared:svc-f (debounced), got %v", got)
	}
}

func TestCreateThenDeleteCoalescesToDisappeared(t *testing.T) {
	dir := t.TempDir()
	var r recorder
	w, err := New(testLogger{}, r.handler(), Options{DebounceInterval: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go w.Run()
	t.Cleanup(w.Close)

	if err := w.AddDir(dir); err != nil {
		t.Fatalf("AddDir: %v", err)
	}
	// Expect ONE final event: disappeared. Create-then-delete inside
	// the debounce window collapses; disappear always wins.
	r.expect(1)
	path := filepath.Join(dir, "svc-g")
	writeFile(t, path, "x")
	time.Sleep(20 * time.Millisecond)
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	r.wait(t, 2*time.Second)
	got := r.snapshot()
	if len(got) != 1 || got[0] != "disappeared:svc-g" {
		t.Fatalf("want [disappeared:svc-g], got %v", got)
	}
}

func TestMultipleDirs(t *testing.T) {
	d1 := t.TempDir()
	d2 := t.TempDir()
	var r recorder
	w := newTestWatcher(t, r.handler())
	if err := w.AddDir(d1); err != nil {
		t.Fatalf("AddDir d1: %v", err)
	}
	if err := w.AddDir(d2); err != nil {
		t.Fatalf("AddDir d2: %v", err)
	}
	r.expect(2)
	writeFile(t, filepath.Join(d1, "svc-h1"), "x")
	writeFile(t, filepath.Join(d2, "svc-h2"), "x")
	r.wait(t, 2*time.Second)
	got := r.snapshot()
	if len(got) != 2 {
		t.Fatalf("want 2 events, got %v", got)
	}
	// Order not guaranteed across dirs; check as a set.
	set := map[string]bool{got[0]: true, got[1]: true}
	if !set["appeared:svc-h1"] || !set["appeared:svc-h2"] {
		t.Fatalf("want both appeared, got %v", got)
	}
}

func TestAddDirRejectsMissing(t *testing.T) {
	var r recorder
	w := newTestWatcher(t, r.handler())
	err := w.AddDir("/nonexistent/does/not/exist")
	if err == nil {
		t.Fatal("want error for missing dir")
	}
}

func TestAddDirRejectsFile(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "file")
	writeFile(t, f, "")
	var r recorder
	w := newTestWatcher(t, r.handler())
	err := w.AddDir(f)
	if err == nil {
		t.Fatal("want error when target is a file")
	}
}

func TestNilCallbacksAreOptional(t *testing.T) {
	dir := t.TempDir()
	// Only Appeared wired; Modified/Disappeared are nil.
	seen := make(chan string, 8)
	w, err := New(testLogger{}, Handler{
		Appeared: func(name string) { seen <- name },
	}, Options{DebounceInterval: 40 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go w.Run()
	t.Cleanup(w.Close)
	if err := w.AddDir(dir); err != nil {
		t.Fatalf("AddDir: %v", err)
	}
	writeFile(t, filepath.Join(dir, "svc-i"), "x")
	select {
	case name := <-seen:
		if name != "svc-i" {
			t.Fatalf("want svc-i, got %s", name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for Appeared")
	}
	// Then remove it — Disappeared is nil, must not panic.
	if err := os.Remove(filepath.Join(dir, "svc-i")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	// If we got here without panic, the nil-callback path is safe.
}

func TestCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	var r recorder
	w := newTestWatcher(t, r.handler())
	if err := w.AddDir(dir); err != nil {
		t.Fatalf("AddDir: %v", err)
	}
	w.Close()
	w.Close() // must not panic or hang
}

func TestIsServiceFile(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"svc", true},
		{"svc-a", true},
		{"my.service", true},
		{"", false},
		{".hidden", false},
		{"foo~", false},
		{"foo.swp", false},
		{"foo.swx", false},
		{"foo.swo", false},
		{"foo.tmp", false},
		{"foo.new", false},
		{"foo.bak", false},
		{"foo.d", false},
	}
	for _, tc := range cases {
		if got := isServiceFile(tc.name); got != tc.want {
			t.Errorf("isServiceFile(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
