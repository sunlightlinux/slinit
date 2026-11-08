package watchdog

import (
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type lateProbe struct {
	mu          sync.Mutex
	waiting     int
	armed       *Feeder
	unavailable error
	got         chan struct{}
}

func newLateForTest(open func(Config) (*Feeder, error)) (*Late, *lateProbe) {
	p := &lateProbe{got: make(chan struct{}, 4)}
	l := NewLate(Config{}, LateHooks{
		Waiting: func(error) {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.waiting++
		},
		Armed: func(f *Feeder) {
			p.mu.Lock()
			p.armed = f
			p.mu.Unlock()
			p.got <- struct{}{}
		},
		Unavailable: func(err error) {
			p.mu.Lock()
			p.unavailable = err
			p.mu.Unlock()
			p.got <- struct{}{}
		},
	})
	l.open = open
	l.poll = 2 * time.Millisecond
	l.grace = 20 * time.Millisecond
	return l, p
}

func (p *lateProbe) wait(t *testing.T) {
	t.Helper()
	select {
	case <-p.got:
	case <-time.After(5 * time.Second):
		t.Fatal("no outcome")
	}
}

func TestLateArmsWhenTheDeviceAppears(t *testing.T) {
	f, _ := newTestFeeder(t, time.Hour)
	var calls atomic.Int32
	l, p := newLateForTest(func(Config) (*Feeder, error) {
		if calls.Add(1) < 4 {
			return nil, ErrNoDevice
		}
		return f, nil
	})
	defer l.Close()
	l.Start(true)
	p.wait(t)
	if p.armed != f || p.waiting != 1 || p.unavailable != nil {
		t.Fatalf("armed=%v waiting=%d unavailable=%v", p.armed, p.waiting, p.unavailable)
	}
	time.Sleep(20 * time.Millisecond)
	if n := calls.Load(); n != 4 {
		t.Errorf("kept looking after arming: %d opens", n)
	}
}

func TestLateArmsAtOnceWhenTheDeviceIsThere(t *testing.T) {
	f, _ := newTestFeeder(t, time.Hour)
	l, p := newLateForTest(func(Config) (*Feeder, error) { return f, nil })
	defer l.Close()
	l.Start(true)
	if len(p.got) != 1 || p.armed != f || p.waiting != 0 {
		t.Fatalf("not armed before Start returned: armed=%v waiting=%d", p.armed, p.waiting)
	}
}

func TestLateGivesUpOnlyAfterBootReady(t *testing.T) {
	l, p := newLateForTest(func(Config) (*Feeder, error) { return nil, ErrNoDevice })
	defer l.Close()
	l.Start(true)
	time.Sleep(60 * time.Millisecond) // three times the grace
	if len(p.got) != 0 {
		t.Fatal("gave up with boot not ready")
	}
	l.BootReady()
	begin := time.Now()
	p.wait(t)
	if !errors.Is(p.unavailable, ErrNoDevice) {
		t.Fatalf("unavailable = %v", p.unavailable)
	}
	if d := time.Since(begin); d < 15*time.Millisecond {
		t.Errorf("gave up %v after boot ready, before the grace", d)
	}
}

func TestLateDoesNotRetryWhenItMustNot(t *testing.T) {
	busy := errors.New("device or resource busy")
	for _, c := range []struct {
		name  string
		err   error
		retry bool
	}{
		{"retry off", ErrNoDevice, false},
		{"device held by someone else", busy, true},
	} {
		var calls atomic.Int32
		l, p := newLateForTest(func(Config) (*Feeder, error) {
			calls.Add(1)
			return nil, c.err
		})
		l.Start(c.retry)
		time.Sleep(20 * time.Millisecond)
		if n := calls.Load(); n != 1 {
			t.Errorf("%s: %d opens", c.name, n)
		}
		if !errors.Is(p.unavailable, c.err) {
			t.Errorf("%s: unavailable = %v", c.name, p.unavailable)
		}
		l.Close()
	}
}

// The failure that resets a machine in the middle of its shutdown.
func TestLateNeverArmsAfterClose(t *testing.T) {
	var calls atomic.Int32
	l, p := newLateForTest(func(Config) (*Feeder, error) {
		calls.Add(1)
		return nil, ErrNoDevice
	})
	l.Start(true)
	time.Sleep(10 * time.Millisecond)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond) // an attempt under way ends
	n := calls.Load()
	time.Sleep(20 * time.Millisecond)
	if got := calls.Load(); got != n {
		t.Errorf("still opening after Close: %d then %d", n, got)
	}
	if p.armed != nil {
		t.Error("armed after Close")
	}
	if done, _ := l.attempt(); !done {
		t.Error("attempt after Close was not refused")
	}
	if got := calls.Load(); got != n {
		t.Error("attempt after Close opened the device")
	}
}

// Close during an open must wait for it and disarm what it armed: not doing so
// leaves a timer running that nobody feeds.
func TestLateCloseDuringOpenDisarms(t *testing.T) {
	f, path := newTestFeeder(t, time.Hour)
	entered, release := make(chan struct{}), make(chan struct{})
	l, p := newLateForTest(func(Config) (*Feeder, error) {
		close(entered)
		<-release
		return f, nil
	})
	go l.Start(true)
	<-entered
	closed := make(chan struct{})
	go func() {
		l.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close did not wait for the open")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-closed
	p.wait(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), string(rune(magicClose))) {
		t.Errorf("device not disarmed: %q", data)
	}
}

func TestLateCloseIdempotentAndNilSafe(t *testing.T) {
	l, _ := newLateForTest(func(Config) (*Feeder, error) { return nil, ErrNoDevice })
	l.Start(false)
	for range 2 {
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
	}
	var none *Late
	none.BootReady()
	if err := none.Close(); err != nil {
		t.Fatal(err)
	}
}

// Only a device not found is worth waiting for. A path given explicitly does
// not fail this way, which keeps --watchdog-device at a missing path meaning off.
func TestOpenMissingDevicesAndErrNoDevice(t *testing.T) {
	_, err := Open(Config{Device: t.TempDir() + "/nope"})
	if err == nil || errors.Is(err, ErrNoDevice) {
		t.Errorf("explicit path: %v", err)
	}
}
