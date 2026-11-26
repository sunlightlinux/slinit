//go:build linux

package recovery

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
	"golang.org/x/sys/unix"
)

// ptyPair opens a pty and returns its master and the slave's path. All
// output written to the slave is collected; seen reports whether s has
// shown up yet.
func ptyPair(t *testing.T) (master *os.File, path string, seen func(s string) bool) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pty here: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var out strings.Builder
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := m.Read(buf)
			if err != nil {
				return
			}
			mu.Lock()
			out.Write(buf[:n])
			mu.Unlock()
		}
	}()
	return m, fmt.Sprintf("/dev/pts/%d", n), func(s string) bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(out.String(), s)
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// acquireOnStart calls the set's OnConsoleAcquire hook the way
// ProcessService.BringUp does: with the scheduling lock held.
type acquireOnStart struct{ set *service.ServiceSet }

func (l acquireOnStart) ServiceEvent(_ service.Service, ev service.ServiceEvent) {
	if ev == service.EventStarted {
		l.set.OnConsoleAcquire()
	}
}

// An open debug menu must not freeze the state machine. The console
// service detaches the debugger with the scheduling lock held, and the
// detach used to wait for the menu to close: 60s per redraw, and no
// limit at all while the operator sat in the menu's shell.
func TestConsoleAcquireDoesNotWaitForAnOpenMenu(t *testing.T) {
	master, path, seen := ptyPair(t)
	set := service.NewServiceSet(logging.New(logging.LevelError))
	boot := service.NewInternalService(set, "boot")
	set.AddService(boot)

	d := NewDebugger(DebuggerOptions{
		ConsolePath: path,
		Timeout:     30 * time.Second,
		StatusFn:    func() StatusSnapshot { return StatusSnapshot{} },
	})
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	set.OnConsoleAcquire = d.StopOrDefer
	boot.Record().AddListener(acquireOnStart{set})

	master.Write([]byte{0x02}) // Ctrl-B
	waitUntil(t, "the menu", func() bool { return seen("BOOT DEBUGGER") })

	started := make(chan struct{})
	go func() {
		set.StartService(boot)
		close(started)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		master.Write([]byte{'c'}) // let the test end
		t.Fatal("the console service waited for the operator to close the menu")
	}
	select {
	case <-d.doneCh:
		t.Fatal("the debugger stopped under an open menu")
	default:
	}

	// Closing the menu completes the deferred stop.
	master.Write([]byte{'c'})
	waitUntil(t, "the deferred stop", func() bool {
		select {
		case <-d.doneCh:
			return true
		default:
			return false
		}
	})
}

// With no menu open the detach is still inline, so the console is back
// in its own mode before the console service's child captures it.
func TestConsoleAcquireStopsInlineWithNoMenu(t *testing.T) {
	_, path, _ := ptyPair(t)
	s, err := os.OpenFile(path, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	before := lflag(t, s)

	d := NewDebugger(DebuggerOptions{
		ConsolePath: path,
		StatusFn:    func() StatusSnapshot { return StatusSnapshot{} },
	})
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	if lflag(t, s)&unix.ECHO != 0 {
		t.Fatal("setup: the debugger did not put the console in raw mode")
	}
	d.StopOrDefer()
	if d.running.Load() {
		t.Fatal("StopOrDefer did not stop the debugger")
	}
	if got := lflag(t, s); got != before {
		t.Errorf("lflag = %#x when StopOrDefer returned, want %#x", got, before)
	}
}

// A menu that times out must not leave its reader behind. The leftover
// reader took the operator's next key and dropped it, so the Ctrl-B
// after a timed-out menu opened nothing (and once the debugger had
// stopped, it was the first key typed at the login prompt that went).
func TestDebugMenuTimeoutLeavesNoReaderBehind(t *testing.T) {
	master, path, _ := ptyPair(t)
	var opened sync.WaitGroup
	opened.Add(2)
	var menus int32
	var mu sync.Mutex
	d := NewDebugger(DebuggerOptions{
		ConsolePath: path,
		Timeout:     200 * time.Millisecond,
		StatusFn: func() StatusSnapshot {
			mu.Lock()
			menus++
			if menus <= 2 {
				opened.Done()
			}
			mu.Unlock()
			return StatusSnapshot{}
		},
	})
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer d.Stop()

	master.Write([]byte{0x02})
	time.Sleep(600 * time.Millisecond) // the menu opens and times out
	master.Write([]byte{0x02})

	done := make(chan struct{})
	go func() {
		opened.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the second Ctrl-B opened no menu: the timed-out menu's reader swallowed it")
	}
}
