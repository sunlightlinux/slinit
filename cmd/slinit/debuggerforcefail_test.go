//go:build linux

package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/recovery"
	"github.com/sunlightlinux/slinit/pkg/service"
	"golang.org/x/sys/unix"
)

// ptyConsole is a pty whose slave stands in for /dev/console. Whatever
// the debugger draws on it is collected from the master.
type ptyConsole struct {
	master *os.File
	path   string
	mu     sync.Mutex
	out    bytes.Buffer
}

func newPtyConsole(t *testing.T) *ptyConsole {
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
	c := &ptyConsole{master: m, path: fmt.Sprintf("/dev/pts/%d", n)}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := m.Read(buf)
			if err != nil {
				return
			}
			c.mu.Lock()
			c.out.Write(buf[:n])
			c.mu.Unlock()
		}
	}()
	return c
}

// waitFor waits until the console has shown s, counting only output
// after the first `from` bytes.
func (c *ptyConsole) waitFor(t *testing.T, from int, s string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		ok := strings.Contains(c.out.String()[from:], s)
		c.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("console never showed %q", s)
}

func (c *ptyConsole) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.Len()
}

func (c *ptyConsole) key(t *testing.T, b byte) {
	t.Helper()
	if _, err := c.master.Write([]byte{b}); err != nil {
		t.Fatal(err)
	}
}

// detachOnStart stands in for ProcessService.BringUp of a console
// service: it detaches the debugger while the scheduling lock is held,
// and waits for the menu to close to do it.
type detachOnStart struct {
	dbg     *recovery.Debugger
	entered chan struct{}
}

func (l *detachOnStart) ServiceEvent(_ service.Service, ev service.ServiceEvent) {
	if ev != service.EventStarted {
		return
	}
	close(l.entered)
	l.dbg.Stop()
}

// The lock cycle from the Ctrl-B menu: the operator has the menu open,
// a console service starts and so waits for the menu with the
// scheduling lock held, and the operator picks [f], whose
// ForceStopService needs that lock. Run inline, [f] never returned, the
// menu never closed, and PID 1 was stuck for good — the menu's own
// timeout cannot fire while [f] is still running.
func TestDebuggerForceFailDoesNotDeadlockAgainstConsoleAcquire(t *testing.T) {
	con := newPtyConsole(t)
	logger := logging.New(logging.LevelError)
	set := service.NewServiceSet(logger)
	boot := service.NewInternalService(set, "boot")
	stuck := service.NewInternalService(set, "stuck")
	set.AddService(boot)
	set.AddService(stuck)

	dbg := recovery.NewDebugger(recovery.DebuggerOptions{
		ConsolePath: con.path,
		Timeout:     30 * time.Second,
		StatusFn: func() recovery.StatusSnapshot {
			return recovery.StatusSnapshot{InProgress: []recovery.ServiceInfo{
				{Name: "stuck", State: "STARTING"},
			}}
		},
		ForceFailFn:     debuggerForceFail(set),
		ShellCandidates: []string{"/bin/false"},
	})
	if err := dbg.Start(); err != nil {
		t.Skipf("debugger on a pty: %v", err)
	}
	l := &detachOnStart{dbg: dbg, entered: make(chan struct{})}
	boot.Record().AddListener(l)

	con.key(t, 0x02) // Ctrl-B
	con.waitFor(t, 0, "BOOT DEBUGGER")

	started := make(chan struct{})
	go func() {
		set.StartService(boot)
		close(started)
	}()
	<-l.entered

	mark := con.len()
	con.key(t, 'f')
	con.waitFor(t, mark, `force-failed "stuck"`)
	con.waitFor(t, mark, "BOOT DEBUGGER") // redrawn, waiting for a key
	con.key(t, 'c')

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the console service never started after the menu closed")
	}
	dbg.Stop()
}
