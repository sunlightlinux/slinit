package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- ParseOOMPolicy ---

func TestParseOOMPolicy(t *testing.T) {
	cases := []struct {
		in   string
		want OOMPolicy
		ok   bool
	}{
		{"", OOMContinue, true},
		{"continue", OOMContinue, true},
		{"stop", OOMStop, true},
		{"kill", OOMKill, true},
		{"crash", OOMContinue, false},
		{"STOP", OOMContinue, false},
	}
	for _, c := range cases {
		got, err := ParseOOMPolicy(c.in)
		if (err == nil) != c.ok {
			t.Errorf("%q: ok=%v want %v err=%v", c.in, err == nil, c.ok, err)
			continue
		}
		if c.ok && got != c.want {
			t.Errorf("%q: got %v want %v", c.in, got, c.want)
		}
	}
}

// --- readOOMCounters parses the kernel-format text file ---

func writeMemoryEvents(t *testing.T, path string, oomKill, oomGroupKill uint64) {
	t.Helper()
	body := "low 0\nhigh 0\nmax 0\noom 0\n" +
		"oom_kill " + itoa(oomKill) + "\noom_group_kill " + itoa(oomGroupKill) + "\n"
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func TestReadOOMCountersHappyPath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "memory.events")
	writeMemoryEvents(t, p, 3, 1)
	got := readOOMCounters(p)
	if got.oomKill != 3 || got.oomGroupKill != 1 {
		t.Errorf("got %+v want {3 1}", got)
	}
}

func TestReadOOMCountersMissing(t *testing.T) {
	got := readOOMCounters("/nonexistent/memory.events")
	if got.oomKill != 0 || got.oomGroupKill != 0 {
		t.Errorf("missing file should yield zeros, got %+v", got)
	}
}

// --- Integration: oom watcher reacts when counter increments ---

func TestOOMWatcherStopsServiceOnPolicyStop(t *testing.T) {
	// 30ms poll so the test isn't slow.
	restore := setOOMPollIntervalForTesting(30 * time.Millisecond)
	defer restore()

	set, _ := newTestSet()
	svc := NewInternalService(set, "oomtest")
	set.AddService(svc)

	cgDir := t.TempDir()
	eventsPath := filepath.Join(cgDir, "memory.events")
	writeMemoryEvents(t, eventsPath, 0, 0)

	svc.Record().SetCgroupPath(cgDir)
	svc.Record().SetOOMPolicy(OOMStop)

	set.StartService(svc)
	if svc.State() != StateStarted {
		t.Fatalf("setup: expected STARTED, got %v", svc.State())
	}

	// Simulate the kernel reporting an OOM kill.
	writeMemoryEvents(t, eventsPath, 1, 0)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if svc.State() == StateStopped {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if svc.State() != StateStopped {
		t.Errorf("OOMStop should drive svc to STOPPED, got %v", svc.State())
	}
}

func TestOOMWatcherInactiveOnPolicyContinue(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "oomcont")
	set.AddService(svc)

	cgDir := t.TempDir()
	svc.Record().SetCgroupPath(cgDir)
	svc.Record().SetOOMPolicy(OOMContinue)

	set.StartService(svc)
	if svc.Record().oomWatch != nil {
		t.Error("OOMContinue should not arm a watcher")
	}
}

func TestOOMWatcherInactiveWithoutCgroup(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "nocg")
	set.AddService(svc)
	svc.Record().SetOOMPolicy(OOMStop) // policy set but no cgroup

	set.StartService(svc)
	if svc.Record().oomWatch != nil {
		t.Error("missing cgroup path should not arm a watcher")
	}
}
