package shutdown

import (
	"syscall"
	"sync/atomic"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// TestExecuteLogsOutUsersBeforeKill verifies that the wtmp logout hook
// and the wtmp shutdown-boundary hook are both invoked, and that both
// fire before KillAllProcesses — otherwise the utmp/wtmp files could be
// on a filesystem that has already been unmounted or whose writers have
// been signalled.
func TestExecuteLogsOutUsersBeforeKill(t *testing.T) {
	origKill := killFunc
	origSync := syncFunc
	origReboot := rebootFunc
	origHook := runHookFunc
	origLogout := logoutAllUsersFunc
	origLogShut := logShutdownFunc
	origGrace := killGracePeriod
	t.Cleanup(func() {
		killFunc = origKill
		syncFunc = origSync
		rebootFunc = origReboot
		runHookFunc = origHook
		logoutAllUsersFunc = origLogout
		logShutdownFunc = origLogShut
		killGracePeriod = origGrace
	})

	var order []string
	var killCalled atomic.Bool
	killFunc = func(pid int, sig syscall.Signal) error {
		if !killCalled.Swap(true) {
			order = append(order, "kill")
		}
		return syscall.ESRCH
	}
	syncFunc = func() {}
	runHookFunc = func(st service.ShutdownType, l *logging.Logger) bool { return true }
	killGracePeriod = 0

	logoutCalls := 0
	logoutAllUsersFunc = func() int {
		logoutCalls++
		order = append(order, "logout")
		return 3
	}
	logShutCalls := 0
	logShutdownFunc = func() bool {
		logShutCalls++
		order = append(order, "logshutdown")
		return true
	}

	done := make(chan struct{})
	go func() {
		rebootFunc = func(cmd int) error {
			close(done)
			select {} // prevent InfiniteHold reaching the goroutine
		}
		Execute(service.ShutdownReboot, logging.New(logging.LevelError))
	}()
	<-done

	if logoutCalls != 1 {
		t.Errorf("LogoutAllUsers calls = %d, want 1", logoutCalls)
	}
	if logShutCalls != 1 {
		t.Errorf("LogShutdown calls = %d, want 1", logShutCalls)
	}

	// Order must be: logout → logshutdown → kill.
	want := []string{"logout", "logshutdown", "kill"}
	if len(order) < len(want) {
		t.Fatalf("order = %v, want prefix %v", order, want)
	}
	for i, w := range want {
		if order[i] != w {
			t.Errorf("order[%d] = %q, want %q (full order %v)", i, order[i], w, order)
		}
	}
}

// TestExecuteLogoutSilentWhenNoSessions covers the common path where
// no one is logged in: LogoutAllUsers returns 0 but is still invoked,
// and LogShutdown still fires (so `last -x` gets its boundary record).
func TestExecuteLogoutSilentWhenNoSessions(t *testing.T) {
	origKill := killFunc
	origSync := syncFunc
	origReboot := rebootFunc
	origHook := runHookFunc
	origLogout := logoutAllUsersFunc
	origLogShut := logShutdownFunc
	origGrace := killGracePeriod
	t.Cleanup(func() {
		killFunc = origKill
		syncFunc = origSync
		rebootFunc = origReboot
		runHookFunc = origHook
		logoutAllUsersFunc = origLogout
		logShutdownFunc = origLogShut
		killGracePeriod = origGrace
	})

	killFunc = func(pid int, sig syscall.Signal) error { return syscall.ESRCH }
	syncFunc = func() {}
	runHookFunc = func(st service.ShutdownType, l *logging.Logger) bool { return true }
	killGracePeriod = 0

	logoutCalls := 0
	logoutAllUsersFunc = func() int { logoutCalls++; return 0 }
	logShutCalls := 0
	logShutdownFunc = func() bool { logShutCalls++; return true }

	done := make(chan struct{})
	go func() {
		rebootFunc = func(cmd int) error {
			close(done)
			select {}
		}
		Execute(service.ShutdownPoweroff, logging.New(logging.LevelError))
	}()
	<-done

	if logoutCalls != 1 {
		t.Errorf("LogoutAllUsers calls = %d, want 1 (should run even with zero sessions)", logoutCalls)
	}
	if logShutCalls != 1 {
		t.Errorf("LogShutdown calls = %d, want 1", logShutCalls)
	}
}
