package service

import (
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// sdNotifyHelper runs as the service when SLINIT_SDNOTIFY_HELPER is set:
// it waits, sends WATCHDOG=1 before READY=1 (which must not count as
// ready), then READY=1, then WATCHDOG=1 every 50ms for a while, then
// falls silent. It writes $WATCHDOG_USEC to the file named by
// SLINIT_SDNOTIFY_OUT.
func sdNotifyHelper() {
	if out := os.Getenv("SLINIT_SDNOTIFY_OUT"); out != "" {
		_ = os.WriteFile(out, []byte(os.Getenv("WATCHDOG_USEC")), 0o644)
	}
	addr := &net.UnixAddr{Name: os.Getenv("NOTIFY_SOCKET"), Net: "unixgram"}
	send := func(s string) {
		if c, err := net.DialUnix("unixgram", nil, addr); err == nil {
			_, _ = c.Write([]byte(s))
			c.Close()
		}
	}
	time.Sleep(300 * time.Millisecond)
	send("WATCHDOG=1")
	time.Sleep(300 * time.Millisecond)
	send("READY=1\nSTATUS=up")
	pings, _ := strconv.Atoi(os.Getenv("SLINIT_SDNOTIFY_PINGS"))
	for i := 0; i < pings; i++ {
		time.Sleep(50 * time.Millisecond)
		send("WATCHDOG=1")
	}
	time.Sleep(time.Hour)
}

func init() {
	if os.Getenv("SLINIT_SDNOTIFY_HELPER") == "1" {
		sdNotifyHelper()
		os.Exit(0)
	}
}

func sdNotifyService(t *testing.T, set *ServiceSet, name string, pings int, watchdog time.Duration) (*ProcessService, string) {
	t.Helper()
	process.SetNotifySocketRoot(t.TempDir())
	out := t.TempDir() + "/wd"
	svc := NewProcessService(set, name)
	svc.SetCommand([]string{"/usr/bin/env", "SLINIT_SDNOTIFY_HELPER=1",
		"SLINIT_SDNOTIFY_OUT=" + out, "SLINIT_SDNOTIFY_PINGS=" + strconv.Itoa(pings), os.Args[0]})
	svc.SetReadyNotificationSD(true)
	svc.SetWatchdogTimeout(watchdog)
	set.AddService(svc)
	return svc, out
}

// ready-notification = sd-notify: STARTING until READY=1 (a WATCHDOG=1
// before it does not count), then STARTED; $WATCHDOG_USEC carries the
// watchdog-timeout.
func TestSDNotifyReadiness(t *testing.T) {
	set, _ := newTestSet()
	svc, out := sdNotifyService(t, set, "sdready", 40, 400*time.Millisecond)
	set.StartService(svc)
	defer func() {
		set.StopService(svc)
		waitForState(t, svc, StateStopped, 5*time.Second)
	}()
	time.Sleep(450 * time.Millisecond) // after the early WATCHDOG=1, before READY=1
	if st := svc.State(); st != StateStarting {
		t.Fatalf("state %v before READY=1, want STARTING", st)
	}
	if st := waitForState(t, svc, StateStarted, 3*time.Second); st != StateStarted {
		t.Fatalf("state %v after READY=1, want STARTED", st)
	}
	if b, _ := os.ReadFile(out); string(b) != "400000" {
		t.Errorf("WATCHDOG_USEC = %q, want 400000", b)
	}
	// The pings keep the watchdog fed: still the same process a while on.
	pid := svc.PID()
	time.Sleep(time.Second)
	if svc.State() != StateStarted || svc.PID() != pid {
		t.Errorf("the watchdog fired although WATCHDOG=1 came every 50ms: state %v pid %d->%d", svc.State(), pid, svc.PID())
	}
}

// A service that stops sending WATCHDOG=1 is caught by the watchdog.
func TestSDNotifyWatchdogMiss(t *testing.T) {
	set, _ := newTestSet()
	svc, _ := sdNotifyService(t, set, "sdmiss", 2, 300*time.Millisecond)
	set.StartService(svc)
	defer func() {
		set.StopService(svc)
		waitForState(t, svc, StateStopped, 5*time.Second)
	}()
	if st := waitForState(t, svc, StateStarted, 3*time.Second); st != StateStarted {
		t.Fatalf("never STARTED: %v", st)
	}
	// Two pings, then silence: the 300ms watchdog fires and stops it.
	if st := waitForState(t, svc, StateStopped, 3*time.Second); st != StateStopped {
		t.Fatalf("state %v after the pings stopped, want the watchdog to have stopped it", st)
	}
}

// A process that is neither the service's main process nor descended
// from it cannot make it ready, though the socket accepts its datagram.
func TestSDNotifyIgnoresStrangers(t *testing.T) {
	set, _ := newTestSet()
	process.SetNotifySocketRoot(t.TempDir())
	svc := NewProcessService(set, "sdstranger")
	svc.SetCommand([]string{"/bin/sleep", "60"})
	svc.SetReadyNotificationSD(true)
	set.AddService(svc)
	set.StartService(svc)
	defer func() {
		set.StopService(svc)
		waitForState(t, svc, StateStopped, 5*time.Second)
	}()
	var sock string
	for deadline := time.Now().Add(2 * time.Second); sock == "" && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		sock = svc.notifySocketPath()
	}
	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("READY=1"))
	time.Sleep(300 * time.Millisecond)
	if st := svc.State(); st != StateStarting {
		t.Fatalf("a READY=1 from the test process (pid %d, not the service's) made it %v", os.Getpid(), st)
	}
}

func openFDs(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("no /proc/self/fd")
	}
	return len(ents)
}

// A start that fails at the fork closes the readiness pipe slinit kept
// for sd-notify; it used to stay open, a pipe per failed start (per
// connection for an on-demand service).
func TestSDNotifyFailedStartLeaksNothing(t *testing.T) {
	set, _ := newTestSet()
	process.SetNotifySocketRoot(t.TempDir())
	svc := NewProcessService(set, "sdmissing")
	svc.SetCommand([]string{"/nonexistent/binary"})
	svc.SetReadyNotificationSD(true)
	set.AddService(svc)
	set.StartService(svc)
	waitForState(t, svc, StateStopped, 2*time.Second)
	before := openFDs(t)
	for i := 0; i < 5; i++ {
		set.StartService(svc)
		waitForState(t, svc, StateStopped, 2*time.Second)
	}
	if after := openFDs(t); after > before {
		t.Errorf("open descriptors %d -> %d after five failed starts", before, after)
	}
}
