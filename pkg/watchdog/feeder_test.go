package watchdog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// newTestFeeder wires a Feeder around a regular file so tests can verify
// Ping / Close behaviour without touching /dev/watchdog. Open() is the
// only path that talks to the kernel ioctl; everything else just writes
// bytes, which works on any *os.File.
func newTestFeeder(t *testing.T, interval time.Duration) (*Feeder, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "wd")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("open temp watchdog file: %v", err)
	}
	return &Feeder{
		cfg: Config{
			Device:   path,
			Timeout:  interval * 3,
			Interval: interval,
		},
		file: f,
	}, path
}

func TestConfigResolveDefaults(t *testing.T) {
	c, err := Config{}.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if c.Timeout != DefaultTimeout {
		t.Errorf("Timeout = %s, want %s", c.Timeout, DefaultTimeout)
	}
	want := DefaultTimeout / DefaultIntervalDivisor
	if c.Interval != want {
		t.Errorf("Interval = %s, want %s", c.Interval, want)
	}
}

func TestConfigResolveExplicitInterval(t *testing.T) {
	c, err := Config{Timeout: 30 * time.Second, Interval: 5 * time.Second}.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if c.Timeout != 30*time.Second || c.Interval != 5*time.Second {
		t.Errorf("got %+v, want timeout=30s interval=5s", c)
	}
}

func TestConfigResolveIntervalGEqTimeoutFails(t *testing.T) {
	if _, err := (Config{Timeout: time.Second, Interval: time.Second}).Resolve(); err == nil {
		t.Fatal("expected error when interval == timeout, got nil")
	}
	if _, err := (Config{Timeout: time.Second, Interval: 2 * time.Second}).Resolve(); err == nil {
		t.Fatal("expected error when interval > timeout, got nil")
	}
}

func TestConfigResolveSubSecondTimeoutClampsInterval(t *testing.T) {
	// timeout=2s/3 = 666ms → fine; but timeout=2s with interval=0 → 666ms
	// which is still < timeout. Tested above. Here we exercise the
	// clamp when the divided value would be 0 (timeout < divisor ns).
	c, err := Config{Timeout: time.Nanosecond}.Resolve()
	if err == nil {
		t.Fatalf("expected error for nanosecond timeout, got cfg=%+v", c)
	}
}

func TestPingWritesKeepAliveByte(t *testing.T) {
	f, path := newTestFeeder(t, 50*time.Millisecond)
	defer f.Close()

	if err := f.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if err := f.Ping(); err != nil {
		t.Fatalf("Ping (2nd): %v", err)
	}

	// After Close, the contents end in 'V' (magic close); tests below
	// cover that. Here we only verify the keep-alive bytes that landed
	// before Close — re-open the file read-only after a sync.
	if err := f.file.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) != 2 || data[0] != keepAliveByte || data[1] != keepAliveByte {
		t.Errorf("file contents = %v, want two keep-alive bytes", data)
	}
}

func TestCloseWritesMagicByte(t *testing.T) {
	f, path := newTestFeeder(t, 50*time.Millisecond)
	if err := f.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) == 0 || data[len(data)-1] != magicClose {
		t.Errorf("file contents = %v, want last byte = 'V'", data)
	}
}

func TestCloseIdempotent(t *testing.T) {
	f, _ := newTestFeeder(t, 50*time.Millisecond)
	if err := f.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Errorf("second Close should be a no-op, got %v", err)
	}
	if err := f.Close(); err != nil {
		t.Errorf("third Close should be a no-op, got %v", err)
	}
}

func TestPingAfterCloseReturnsClosedSentinel(t *testing.T) {
	f, _ := newTestFeeder(t, 50*time.Millisecond)
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err := f.Ping()
	if !errors.Is(err, errClosed) {
		t.Errorf("Ping after Close: got %v, want errClosed", err)
	}
}

func TestRunCancellation(t *testing.T) {
	f, _ := newTestFeeder(t, 20*time.Millisecond)
	defer f.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()

	// Let the ticker fire at least once, then cancel.
	time.Sleep(60 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run after cancel: %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s after cancel")
	}
}

func TestRunStopsWhenClosedConcurrently(t *testing.T) {
	f, _ := newTestFeeder(t, 20*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-done:
		// Either nil (ticker fired post-close, hit errClosed → nil) or
		// the ctx hasn't fired yet so Run is still spinning. We expect
		// the close→errClosed path here, so a non-nil error is a bug.
		if err != nil {
			t.Errorf("Run after Close: %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s after Close")
	}
}

func TestPingConcurrent(t *testing.T) {
	f, _ := newTestFeeder(t, time.Second)
	defer f.Close()

	const goroutines = 8
	const pingsEach = 32

	var wg atomic.Int32
	wg.Store(goroutines)
	errs := make(chan error, goroutines*pingsEach)
	done := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		go func() {
			for j := 0; j < pingsEach; j++ {
				if err := f.Ping(); err != nil {
					errs <- err
				}
			}
			if wg.Add(-1) == 0 {
				close(done)
			}
		}()
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent pings did not complete in 2s")
	}
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Ping: %v", err)
	}
}

func TestDeviceTimeoutIntervalAccessors(t *testing.T) {
	f, path := newTestFeeder(t, 100*time.Millisecond)
	defer f.Close()

	if got := f.Device(); got != path {
		t.Errorf("Device() = %q, want %q", got, path)
	}
	if got := f.Timeout(); got != 300*time.Millisecond {
		t.Errorf("Timeout() = %s, want 300ms", got)
	}
	if got := f.Interval(); got != 100*time.Millisecond {
		t.Errorf("Interval() = %s, want 100ms", got)
	}
}

func TestOpenMissingDeviceReturnsError(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "no-such-watchdog")
	_, err := Open(Config{Device: missing, Timeout: 30 * time.Second})
	if err == nil {
		t.Fatalf("Open(missing) returned nil error")
	}
}

func TestOpenAutoDiscoveryNoneFound(t *testing.T) {
	// Run only when neither watchdog device exists on the test host.
	// On a developer laptop both files are typically absent; on a host
	// running slinit they exist, in which case we skip — Open with an
	// empty Device would actually succeed and arm the kernel timer.
	if _, err := os.Stat(DefaultDevice); err == nil {
		t.Skipf("%s exists, skipping no-device assertion", DefaultDevice)
	}
	if _, err := os.Stat(FallbackDevice); err == nil {
		t.Skipf("%s exists, skipping no-device assertion", FallbackDevice)
	}
	if _, err := Open(Config{}); err == nil {
		t.Fatal("Open with no devices present returned nil error")
	}
}
