package control

import (
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestScheduleShutdownImmediate(t *testing.T) {
	logger := logging.New(logging.LevelError)
	srv := NewServer(nil, "/dev/null", logger)

	var called bool
	var calledType service.ShutdownType
	srv.ShutdownFunc = func(st service.ShutdownType) {
		called = true
		calledType = st
	}

	srv.ScheduleShutdown(service.ShutdownReboot, 0)

	if !called {
		t.Fatal("ShutdownFunc not called for delay=0")
	}
	if calledType != service.ShutdownReboot {
		t.Errorf("type = %v, want ShutdownReboot", calledType)
	}
}

func TestScheduleShutdownDelayed(t *testing.T) {
	logger := logging.New(logging.LevelError)
	srv := NewServer(nil, "/dev/null", logger)

	done := make(chan struct{})
	srv.ShutdownFunc = func(st service.ShutdownType) {
		close(done)
	}

	srv.ScheduleShutdown(service.ShutdownPoweroff, 100*time.Millisecond)

	// Should not fire immediately.
	select {
	case <-done:
		t.Fatal("ShutdownFunc called before delay expired")
	default:
	}

	// Query should show pending.
	st, remaining, ok := srv.ScheduledShutdownInfo()
	if !ok {
		t.Fatal("no scheduled shutdown reported")
	}
	if st != service.ShutdownPoweroff {
		t.Errorf("type = %v, want ShutdownPoweroff", st)
	}
	if remaining <= 0 || remaining > 200*time.Millisecond {
		t.Errorf("remaining = %v, expected ~100ms", remaining)
	}

	// Wait for it to fire.
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ShutdownFunc not called after delay")
	}

	// Should no longer be pending.
	_, _, ok = srv.ScheduledShutdownInfo()
	if ok {
		t.Error("shutdown still pending after execution")
	}
}

func TestCancelShutdown(t *testing.T) {
	logger := logging.New(logging.LevelError)
	srv := NewServer(nil, "/dev/null", logger)

	fired := make(chan struct{}, 1)
	srv.ShutdownFunc = func(st service.ShutdownType) {
		fired <- struct{}{}
	}

	srv.ScheduleShutdown(service.ShutdownHalt, 500*time.Millisecond)

	// Cancel it.
	ok := srv.CancelShutdown()
	if !ok {
		t.Fatal("CancelShutdown returned false")
	}

	// Should no longer be pending.
	_, _, pending := srv.ScheduledShutdownInfo()
	if pending {
		t.Error("shutdown still pending after cancel")
	}

	// Wait past the original deadline — should NOT fire.
	select {
	case <-fired:
		t.Fatal("ShutdownFunc called after cancel")
	case <-time.After(600 * time.Millisecond):
	}
}

func TestCancelShutdownNoPending(t *testing.T) {
	logger := logging.New(logging.LevelError)
	srv := NewServer(nil, "/dev/null", logger)

	ok := srv.CancelShutdown()
	if ok {
		t.Error("CancelShutdown returned true with no pending shutdown")
	}
}

func TestScheduleShutdownReplace(t *testing.T) {
	logger := logging.New(logging.LevelError)
	srv := NewServer(nil, "/dev/null", logger)

	typeCh := make(chan service.ShutdownType, 1)
	srv.ShutdownFunc = func(st service.ShutdownType) {
		typeCh <- st
	}

	// Schedule halt in 500ms.
	srv.ScheduleShutdown(service.ShutdownHalt, 500*time.Millisecond)
	// Replace with reboot in 100ms.
	srv.ScheduleShutdown(service.ShutdownReboot, 100*time.Millisecond)

	select {
	case got := <-typeCh:
		if got != service.ShutdownReboot {
			t.Errorf("type = %v, want ShutdownReboot (replacement)", got)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement shutdown did not fire")
	}
}

func TestShutdownTypeName(t *testing.T) {
	cases := []struct {
		st   service.ShutdownType
		want string
	}{
		{service.ShutdownHalt, "halt"},
		{service.ShutdownPoweroff, "poweroff"},
		{service.ShutdownReboot, "reboot"},
		{service.ShutdownKexec, "kexec"},
		{service.ShutdownSoftReboot, "softreboot"},
	}
	for _, tc := range cases {
		got := shutdownTypeName(tc.st)
		if got != tc.want {
			t.Errorf("shutdownTypeName(%v) = %q, want %q", tc.st, got, tc.want)
		}
	}
}
