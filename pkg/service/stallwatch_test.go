package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The shared testLogger appends to a plain slice and stores the format
// string rather than the message. The watchdog reports from its own
// goroutine and the text is the point, so these tests bring their own.
type stallLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *stallLogger) ServiceStarted(string)        {}
func (l *stallLogger) ServiceStopped(string)        {}
func (l *stallLogger) ServiceFailed(string, bool)   {}
func (l *stallLogger) Info(string, ...interface{})  {}
func (l *stallLogger) Error(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}
func (l *stallLogger) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// A held lock must be reported. This is the test for the thing that was
// missing when the path-activation stall happened: PID 1 was wedged for
// twelve minutes and said nothing.
func TestStallIsReportedWhileTheLockIsHeld(t *testing.T) {
	set := NewServiceSet(&stallLogger{})

	var reports atomic.Int32
	var lastHeld atomic.Int64
	stop := set.watchForStalls(10*time.Millisecond, 50*time.Millisecond,
		func(held time.Duration) {
			lastHeld.Store(int64(held))
			reports.Add(1)
		})
	defer stop()

	release := make(chan struct{})
	held := make(chan struct{})
	go func() {
		set.WithGraphLock(func() {
			close(held)
			<-release
		})
	}()
	<-held

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && reports.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	close(release)

	if reports.Load() == 0 {
		t.Fatal("the lock was held well past the threshold and nothing was reported")
	}
	if d := time.Duration(lastHeld.Load()); d < 50*time.Millisecond {
		t.Errorf("reported a hold of %v, which is below the threshold it was given", d)
	}
}

// And a lock that is merely busy must NOT be reported. The state machine
// takes this lock for every transition, so losing a probe is the normal
// case; only never winning one is a stall. A watchdog that cried wolf on
// a busy boot would be turned off and then be useless.
func TestBusyLockIsNotReportedAsAStall(t *testing.T) {
	set := NewServiceSet(&stallLogger{})

	var reports atomic.Int32
	stop := set.watchForStalls(5*time.Millisecond, 50*time.Millisecond,
		func(time.Duration) { reports.Add(1) })
	defer stop()

	// Hammer the lock for well past the threshold, but never hold it.
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			set.WithGraphLock(func() {})
		}
	}()
	time.Sleep(500 * time.Millisecond)
	close(done)

	if n := reports.Load(); n != 0 {
		t.Errorf("%d stall report(s) for a lock that was busy but never held", n)
	}
}

// Recovery has to be visible too: a long legitimate hold should leave
// both a complaint and the line that says it ended, or reading the log
// later cannot tell a resolved episode from a dead system.
func TestStallRecoveryIsLogged(t *testing.T) {
	logger := &stallLogger{}
	set := NewServiceSet(logger)

	stop := set.watchForStalls(10*time.Millisecond, 30*time.Millisecond,
		func(held time.Duration) { set.reportStall(held, "") })
	defer stop()

	release := make(chan struct{})
	held := make(chan struct{})
	go func() {
		set.WithGraphLock(func() {
			close(held)
			<-release
		})
	}()
	<-held
	time.Sleep(300 * time.Millisecond)
	close(release)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logger.String(), "free again after") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("no recovery line after the lock was released; log was:\n%s", logger.String())
}

// The dump goes to a file, because the stderr notice is lost if what is
// stuck is a write to the console.
func TestStallDumpLandsInTheFile(t *testing.T) {
	set := NewServiceSet(&stallLogger{})
	path := filepath.Join(t.TempDir(), "stall.stack")

	set.reportStall(42*time.Second, path)

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no dump file: %v", err)
	}
	body := string(b)
	for _, want := range []string{"held-for: 42s", "goroutine "} {
		if !strings.Contains(body, want) {
			t.Errorf("dump does not contain %q; it was:\n%s", want, body[:min(len(body), 400)])
		}
	}
}
