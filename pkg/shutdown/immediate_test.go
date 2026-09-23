package shutdown

import (
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// ExecuteImmediate is `slinitctl shutdown <type> --superfast`: the reboot
// syscall with nothing in front of it. What it skips is the whole point,
// so the test asserts the omissions — a sync or a utmp write creeping
// back in would make it no faster than ExecuteForce, and the flag would
// be a lie.
func TestExecuteImmediateSkipsEverythingButTheSyscall(t *testing.T) {
	origSync, origReboot := syncFunc, rebootFunc
	origLogout, origLogShut := logoutAllUsersFunc, logShutdownFunc
	origKill := killFunc
	t.Cleanup(func() {
		syncFunc, rebootFunc = origSync, origReboot
		logoutAllUsersFunc, logShutdownFunc = origLogout, origLogShut
		killFunc = origKill
	})

	var synced, loggedOut, wroteUtmp, killed atomic.Bool
	syncFunc = func() { synced.Store(true) }
	logoutAllUsersFunc = func() int { loggedOut.Store(true); return 0 }
	logShutdownFunc = func() bool { wroteUtmp.Store(true); return true }
	killFunc = func(int, syscall.Signal) error { killed.Store(true); return nil }

	var gotCmd atomic.Int64
	done := make(chan struct{})
	go func() {
		rebootFunc = func(cmd int) error {
			gotCmd.Store(int64(cmd))
			close(done)
			select {} // never return: the real syscall doesn't either
		}
		ExecuteImmediate(service.ShutdownPoweroff, logging.New(logging.LevelError))
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ExecuteImmediate never reached the reboot syscall")
	}

	if got := gotCmd.Load(); got != int64(syscall.LINUX_REBOOT_CMD_POWER_OFF) {
		t.Errorf("reboot cmd = %#x, want POWER_OFF (%#x)", got, syscall.LINUX_REBOOT_CMD_POWER_OFF)
	}
	if synced.Load() {
		t.Error("filesystems were synced: that is ExecuteForce's job, not this path's")
	}
	if loggedOut.Load() || wroteUtmp.Load() {
		t.Error("utmp/wtmp was touched; --superfast is meant to do nothing but the syscall")
	}
	if killed.Load() {
		t.Error("processes were signalled; the syscall is what stops them here")
	}
}
