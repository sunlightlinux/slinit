package service

import (
	"os"
	"testing"
	"time"
)

// The s6 readiness convention is a newline, not merely a byte.
//
// s6-linux-init's own reader loops read() until memchr(buf, '\n', r)
// succeeds; dinit adopted the same rule in 5d5ea44 after taking any
// byte for years, and slinit had the identical gap. These cases pin the
// four writer shapes that distinguish the two rules.
//
// watchReadyPipe is exercised directly rather than through a service
// start, because what is under test is the pipe protocol and a real
// start would also drag in exec, timers and the state machine — the
// shape that makes a failure ambiguous.
func readyResult(t *testing.T, write func(*os.File), wait time.Duration) (bool, bool) {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()

	svc := NewProcessService(NewServiceSet(depTestLogger{}), "s6-readiness-test")

	ch := make(chan bool, 1)
	go svc.watchReadyPipe(pr, ch)
	go func() {
		defer pw.Close()
		write(pw)
	}()

	select {
	case r := <-ch:
		return r, true
	case <-time.After(wait):
		return false, false // still waiting: no readiness signalled
	}
}

func TestS6ReadinessNewlineSignals(t *testing.T) {
	ready, decided := readyResult(t, func(w *os.File) {
		w.Write([]byte("ready\n"))
		time.Sleep(2 * time.Second)
	}, 2*time.Second)
	if !decided || !ready {
		t.Errorf("a newline must signal readiness (decided=%v ready=%v)", decided, ready)
	}
}

// The case that proves the loop is necessary and not polish: a single
// Read would see "rea", find no newline, and have to decide without the
// rest of the signal.
func TestS6ReadinessSplitAcrossWrites(t *testing.T) {
	ready, decided := readyResult(t, func(w *os.File) {
		w.Write([]byte("rea"))
		time.Sleep(50 * time.Millisecond)
		w.Write([]byte("dy\n"))
		time.Sleep(2 * time.Second)
	}, 2*time.Second)
	if !decided || !ready {
		t.Errorf("a signal split across writes must still be seen (decided=%v ready=%v)", decided, ready)
	}
}

// The behaviour change: bytes with no newline, from a service that
// keeps running, are NOT readiness. This is what slinit's man page used
// to document as "write a single byte", and what made any stray write
// to that fd indistinguishable from a ready signal.
func TestS6ReadinessBytesWithoutNewlineDoNotSignal(t *testing.T) {
	for _, tc := range []struct{ name, payload string }{
		{"single byte", "x"},
		{"progress line", "starting up..."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, decided := readyResult(t, func(w *os.File) {
				w.Write([]byte(tc.payload))
				time.Sleep(2 * time.Second)
			}, 400*time.Millisecond)
			if decided {
				t.Errorf("%q signalled readiness; only a newline may", tc.payload)
			}
		})
	}
}

// A pipe closed without a newline is a definite negative, not a wait:
// the service is gone and will never signal.
func TestS6ReadinessEOFWithoutNewlineIsNotReady(t *testing.T) {
	ready, decided := readyResult(t, func(w *os.File) {
		w.Write([]byte("x"))
	}, 2*time.Second)
	if !decided {
		t.Fatal("EOF must resolve the wait rather than hang")
	}
	if ready {
		t.Error("a pipe closed with no newline must not count as ready")
	}
}
