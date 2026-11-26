package watchdog

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

// fakeTimeoutIoctls stands in for WDIOC_SETTIMEOUT/GETTIMEOUT: the
// device takes the timeout it is given, capped at max seconds, or
// refuses with setErr.
func fakeTimeoutIoctls(t *testing.T, max int, setErr error) {
	t.Helper()
	oldSet, oldGet := setTimeoutIoctl, getTimeoutIoctl
	t.Cleanup(func() { setTimeoutIoctl, getTimeoutIoctl = oldSet, oldGet })
	cur := 0
	setTimeoutIoctl = func(fd, secs int) error {
		if setErr != nil {
			return setErr
		}
		cur = min(secs, max)
		return nil
	}
	getTimeoutIoctl = func(fd int) (int, error) { return cur, nil }
}

func lastByte(t *testing.T, path string) byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("nothing was written to the device")
	}
	return data[len(data)-1]
}

// The final shutdown of a reboot used to run with the watchdog
// magic-closed, so a hang in the kill-all, an unmount or the sync held
// the machine for ever. Handover leaves it armed with the long timeout:
// no 'V', the fd still open, and nobody feeding it any more.
func TestHandoverLeavesTheDeviceArmedAndUnfed(t *testing.T) {
	fakeTimeoutIoctls(t, 3600, nil)
	f, path := newTestFeeder(t, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()
	time.Sleep(30 * time.Millisecond)

	file, err := f.Handover(10 * time.Minute)
	if err != nil {
		t.Fatalf("Handover: %v", err)
	}
	if file == nil {
		t.Fatal("Handover returned no device")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run after Handover: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("the feeder kept running after Handover")
	}
	if b := lastByte(t, path); b == magicClose {
		t.Fatal("Handover magic-closed the device")
	}
	if _, err := file.Write([]byte{keepAliveByte}); err != nil {
		t.Errorf("the handed-over device is not open: %v", err)
	}
	// Close afterwards must not disarm what was handed over.
	if err := f.Close(); err != nil {
		t.Errorf("Close after Handover: %v", err)
	}
	if b := lastByte(t, path); b == magicClose {
		t.Error("Close after Handover disarmed the device")
	}
	if !errors.Is(f.Ping(), errClosed) {
		t.Error("Ping after Handover still feeds the device")
	}
}

// A device that will not take the long timeout must not stay armed with
// its short one while nothing feeds it: that resets the machine in the
// middle of a healthy shutdown.
func TestHandoverDisarmsWhenTheTimeoutIsNotTaken(t *testing.T) {
	for _, c := range []struct {
		name   string
		max    int
		setErr error
	}{
		{"refused", 3600, syscall.EINVAL},
		{"clamped", 60, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeTimeoutIoctls(t, c.max, c.setErr)
			f, path := newTestFeeder(t, 10*time.Millisecond)
			file, err := f.Handover(10 * time.Minute)
			if err == nil || file != nil {
				t.Fatalf("Handover = %v, %v; want an error and no device", file, err)
			}
			if b := lastByte(t, path); b != magicClose {
				t.Errorf("last byte %q, want the magic close", b)
			}
		})
	}
}

func TestLateHandover(t *testing.T) {
	fakeTimeoutIoctls(t, 3600, nil)
	f, path := newTestFeeder(t, 10*time.Millisecond)
	l, p := newLateForTest(func(Config) (*Feeder, error) { return f, nil })
	l.Start(false)
	p.wait(t)

	file, err := l.Handover(10 * time.Minute)
	if err != nil || file == nil {
		t.Fatalf("Handover = %v, %v", file, err)
	}
	if err := l.Close(); err != nil {
		t.Errorf("Close after Handover: %v", err)
	}
	if b := lastByte(t, path); b == magicClose {
		t.Error("Close after Handover disarmed the device")
	}

	// No device yet: nothing to hand over, and none is armed later.
	opens := 0
	l2, _ := newLateForTest(func(Config) (*Feeder, error) {
		opens++
		return nil, ErrNoDevice
	})
	if file, err := l2.Handover(time.Minute); file != nil || err != nil {
		t.Errorf("Handover with no device = %v, %v; want nil, nil", file, err)
	}
	l2.Start(true)
	if opens != 0 {
		t.Error("armed after Handover")
	}

	var nilLate *Late
	if file, err := nilLate.Handover(time.Minute); file != nil || err != nil {
		t.Errorf("nil Late Handover = %v, %v", file, err)
	}
}
