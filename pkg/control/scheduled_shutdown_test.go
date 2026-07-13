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

	srv.ScheduleShutdown(service.ShutdownReboot, 0, "")

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

	srv.ScheduleShutdown(service.ShutdownPoweroff, 100*time.Millisecond, "")

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

	srv.ScheduleShutdown(service.ShutdownHalt, 500*time.Millisecond, "")

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
	srv.ScheduleShutdown(service.ShutdownHalt, 500*time.Millisecond, "")
	// Replace with reboot in 100ms.
	srv.ScheduleShutdown(service.ShutdownReboot, 100*time.Millisecond, "")

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

// TestScheduleShutdownPassesMessageToWallFunc verifies the message
// argument flows through to WallFunc when the shutdown is scheduled.
// Operators depend on WallFunc for the initial notice broadcast, so a
// dropped message here would break the entire -m/--message contract.
func TestScheduleShutdownPassesMessageToWallFunc(t *testing.T) {
	logger := logging.New(logging.LevelError)
	srv := NewServer(nil, "/dev/null", logger)

	var got string
	srv.WallFunc = func(_ service.ShutdownType, _ time.Duration, _ bool, msg string) {
		got = msg
	}

	srv.ScheduleShutdown(service.ShutdownHalt, 5*time.Second, "planned maintenance")
	if got != "planned maintenance" {
		t.Errorf("WallFunc msg = %q, want %q", got, "planned maintenance")
	}
	// Cleanup — the timer would otherwise fire after the test ends.
	srv.CancelShutdown()
}

// TestScheduleShutdownRemindersFireForLongDelay: a scheduled shutdown
// > 5m should register the full 5m/2m/1m reminder chain.
func TestScheduleShutdownRemindersFireForLongDelay(t *testing.T) {
	logger := logging.New(logging.LevelError)
	srv := NewServer(nil, "/dev/null", logger)
	srv.WallFunc = func(_ service.ShutdownType, _ time.Duration, _ bool, _ string) {}
	srv.WallReminderFunc = func(_ service.ShutdownType, _ time.Duration, _ string) {}

	srv.ScheduleShutdown(service.ShutdownReboot, 10*time.Minute, "reason")

	srv.scheduledMu.Lock()
	n := len(srv.scheduledReminders)
	srv.scheduledMu.Unlock()

	if n != 3 {
		t.Errorf("reminder count = %d, want 3 (5m/2m/1m)", n)
	}
	srv.CancelShutdown()
}

// TestScheduleShutdownRemindersSkipShortDelay: with only 90s to go,
// the 5m and 2m reminders would fire in the past — only 1m survives.
func TestScheduleShutdownRemindersSkipShortDelay(t *testing.T) {
	logger := logging.New(logging.LevelError)
	srv := NewServer(nil, "/dev/null", logger)
	srv.WallFunc = func(_ service.ShutdownType, _ time.Duration, _ bool, _ string) {}
	srv.WallReminderFunc = func(_ service.ShutdownType, _ time.Duration, _ string) {}

	srv.ScheduleShutdown(service.ShutdownReboot, 90*time.Second, "")

	srv.scheduledMu.Lock()
	n := len(srv.scheduledReminders)
	srv.scheduledMu.Unlock()

	if n != 1 {
		t.Errorf("reminder count = %d, want 1 (only 1m fits in a 90s window)", n)
	}
	srv.CancelShutdown()
}

// TestCancelShutdownClearsReminders confirms Cancel stops every
// pending reminder timer. Without this the cancelled shutdown would
// still keep walling countdowns to users, which is worse than the
// alternative.
func TestCancelShutdownClearsReminders(t *testing.T) {
	logger := logging.New(logging.LevelError)
	srv := NewServer(nil, "/dev/null", logger)
	srv.WallFunc = func(_ service.ShutdownType, _ time.Duration, _ bool, _ string) {}
	srv.WallReminderFunc = func(_ service.ShutdownType, _ time.Duration, _ string) {}

	srv.ScheduleShutdown(service.ShutdownReboot, 15*time.Minute, "")
	srv.CancelShutdown()

	srv.scheduledMu.Lock()
	n := len(srv.scheduledReminders)
	timer := srv.scheduledTimer
	srv.scheduledMu.Unlock()

	if n != 0 {
		t.Errorf("reminder slots after cancel = %d, want 0", n)
	}
	if timer != nil {
		t.Error("scheduledTimer should be nil after cancel")
	}
}
