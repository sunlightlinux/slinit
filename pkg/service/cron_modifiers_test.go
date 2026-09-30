package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withCronPersistDir points the on-disk last-run store at a temp dir for
// the duration of a test and restores it afterwards.
func withCronPersistDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := cronPersistDir
	SetCronPersistDir(dir)
	t.Cleanup(func() { SetCronPersistDir(prev) })
	return dir
}

// The bug this whole change started from: jitter reached only the
// calendar loop, so in interval mode the directive parsed and did
// nothing. The runner must now consult it.
//
// Asserting on wall-clock delay would be flaky, so this checks the thing
// the loop actually calls, with a bound tight enough that ignoring the
// setting cannot pass: a runner that dropped it returns 0 every time.
func TestCronIntervalModeAppliesJitter(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "jitter-interval")

	cr := NewCronRunner(svc, []string{"/bin/true"}, time.Hour, 0, "continue", set.logger)
	cr.SetRandomizedDelay(time.Minute, false)

	var sawNonZero bool
	for i := 0; i < 200; i++ {
		j := cr.jitterFor()
		if j < 0 || j >= time.Minute {
			t.Fatalf("offset %v outside [0, 1m)", j)
		}
		if j > 0 {
			sawNonZero = true
		}
	}
	if !sawNonZero {
		t.Error("200 draws all returned 0 — the bound is not being used")
	}

	// And nothing configured means no jitter at all, so a plain interval
	// service keeps firing exactly on its period.
	plain := NewCronRunner(svc, []string{"/bin/true"}, time.Hour, 0, "continue", set.logger)
	if j := plain.jitterFor(); j != 0 {
		t.Errorf("unconfigured jitter should be 0, got %v", j)
	}
}

// cron-fixed-random-delay must return the *same* offset every time, and
// a different one for a different service on the same host — otherwise
// every timer on the box would share one slot.
func TestCronFixedRandomDelayIsStable(t *testing.T) {
	if _, err := os.ReadFile("/etc/machine-id"); err != nil {
		t.Skipf("no /etc/machine-id: %v", err)
	}
	set, _ := newTestSet()

	newFixed := func(name string) *CronRunner {
		cr := NewCronRunner(NewInternalService(set, name), []string{"/bin/true"},
			time.Hour, 0, "continue", set.logger)
		cr.SetRandomizedDelay(time.Hour, true)
		return cr
	}

	a := newFixed("fixed-a")
	first := a.jitterFor()
	for i := 0; i < 20; i++ {
		if got := a.jitterFor(); got != first {
			t.Fatalf("fixed offset drifted: %v then %v", first, got)
		}
	}
	if first < 0 || first >= time.Hour {
		t.Errorf("offset %v outside [0, 1h)", first)
	}

	// A second runner for the same service, as after a restart, must
	// land on the same offset — that is what "fixed" buys.
	if again := newFixed("fixed-a").jitterFor(); again != first {
		t.Errorf("offset not reproducible across runners: %v vs %v", first, again)
	}

	// Two services on one host must not share a slot.
	var distinct bool
	for _, name := range []string{"fixed-b", "fixed-c", "fixed-d", "fixed-e"} {
		if newFixed(name).jitterFor() != first {
			distinct = true
			break
		}
	}
	if !distinct {
		t.Error("every service hashed to the same offset — the name is not in the key")
	}
}

// AccuracySec= is a window that opens at the nominal time. Snapping down
// fired before the calendar said to, which is what this used to do.
func TestSnapToAccuracyNeverFiresEarly(t *testing.T) {
	base := time.Date(2026, 6, 13, 12, 34, 56, 0, time.UTC)

	cases := []struct {
		acc  time.Duration
		want time.Time
	}{
		{time.Minute, time.Date(2026, 6, 13, 12, 35, 0, 0, time.UTC)},
		{time.Hour, time.Date(2026, 6, 13, 13, 0, 0, 0, time.UTC)},
		{15 * time.Minute, time.Date(2026, 6, 13, 12, 45, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		got := snapToAccuracy(base, c.acc)
		if !got.Equal(c.want) {
			t.Errorf("accuracy %v: got %v want %v", c.acc, got, c.want)
		}
		if got.Before(base) {
			t.Errorf("accuracy %v: snapped backwards to %v, before %v", c.acc, got, base)
		}
	}

	// A time already on a boundary must not be pushed a whole bucket on.
	onBoundary := time.Date(2026, 6, 13, 13, 0, 0, 0, time.UTC)
	if got := snapToAccuracy(onBoundary, time.Hour); !got.Equal(onBoundary) {
		t.Errorf("already aligned: got %v want %v", got, onBoundary)
	}
	// Zero disables.
	if got := snapToAccuracy(base, 0); !got.Equal(base) {
		t.Errorf("accuracy 0 should be a no-op, got %v", got)
	}
}

// Catch-up after a reboot is the point of cron-persistent. The store is
// a file, so a missed fire can be staged by writing one — no reboot
// needed to test the behaviour a reboot produces.
func TestCronPersistentCatchesUpInIntervalMode(t *testing.T) {
	dir := withCronPersistDir(t)
	set, _ := newTestSet()
	svc := NewInternalService(set, "catchup")

	marker := filepath.Join(t.TempDir(), "ran")
	cr := NewCronRunner(svc, []string{"/bin/sh", "-c", "echo ok > " + marker},
		time.Hour, 10*time.Second, "continue", set.logger)
	cr.SetPersistent(true)

	// Last run two hours ago with a one-hour period: a fire was missed.
	// The ten-second initial delay must not hold the catch-up back.
	stale := time.Now().Add(-2 * time.Hour)
	if err := os.WriteFile(filepath.Join(dir, "catchup"),
		[]byte(stale.Format(time.RFC3339Nano)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cr.Start()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cr.Stop()

	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("no catch-up run: %v (the stale record should have forced one)", err)
	}

	// And the record must have been refreshed, or every later start
	// would catch up again for ever.
	got, ok := cr.readPersisted()
	if !ok {
		t.Fatal("last-run record missing after the catch-up run")
	}
	if !got.After(stale) {
		t.Errorf("record not advanced: still %v", got)
	}
}

// The other half: a record that is recent enough must NOT trigger a run,
// or a service restart would fire the job every time.
func TestCronPersistentNoCatchUpWhenCurrent(t *testing.T) {
	dir := withCronPersistDir(t)
	set, _ := newTestSet()
	svc := NewInternalService(set, "no-catchup")

	marker := filepath.Join(t.TempDir(), "ran")
	cr := NewCronRunner(svc, []string{"/bin/sh", "-c", "echo ok > " + marker},
		time.Hour, time.Hour, "continue", set.logger)
	cr.SetPersistent(true)

	// Ran a minute ago on a one-hour period: nothing is due.
	if err := os.WriteFile(filepath.Join(dir, "no-catchup"),
		[]byte(time.Now().Add(-time.Minute).Format(time.RFC3339Nano)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cr.Start()
	time.Sleep(400 * time.Millisecond)
	cr.Stop()

	if _, err := os.Stat(marker); err == nil {
		t.Error("ran although the period had not elapsed — a restart would fire the job every time")
	}
}

// An unreadable or garbage record must read as "no previous run" rather
// than propagating an error or, worse, a zero time that looks like 1970
// and makes everything overdue for ever.
func TestCronPersistedRecordGarbage(t *testing.T) {
	dir := withCronPersistDir(t)
	set, _ := newTestSet()
	cr := NewCronRunner(NewInternalService(set, "garbage"), []string{"/bin/true"},
		time.Hour, 0, "continue", set.logger)

	if _, ok := cr.readPersisted(); ok {
		t.Error("a missing record should read as absent")
	}
	if err := os.WriteFile(filepath.Join(dir, "garbage"), []byte("not a timestamp\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, ok := cr.readPersisted(); ok {
		t.Error("an unparseable record should read as absent, not as the zero time")
	}

	// Round-trip a real one.
	want := time.Now().Truncate(time.Millisecond)
	cr.writePersisted(want)
	got, ok := cr.readPersisted()
	if !ok {
		t.Fatal("round-trip failed")
	}
	if !got.Equal(want) {
		t.Errorf("round-trip: got %v want %v", got, want)
	}
}

// SetCronModifiers is the single place the loader applies these, and the
// original defect was exactly that interval mode went through a path
// that did not. Both modes must come out carrying all four.
func TestSetCronModifiersReachesBothModes(t *testing.T) {
	set, _ := newTestSet()

	check := func(mode string, s *ProcessService) {
		t.Helper()
		s.SetCronModifiers(90*time.Second, true, true, 5*time.Second)
		cr := s.cronRunner
		if cr == nil {
			t.Fatalf("%s: no cron runner", mode)
		}
		if cr.randomizedDelay != 90*time.Second {
			t.Errorf("%s: randomizedDelay = %v, want 1m30s", mode, cr.randomizedDelay)
		}
		if !cr.fixedRandomDelay {
			t.Errorf("%s: fixedRandomDelay not set", mode)
		}
		if !cr.persistent {
			t.Errorf("%s: persistent not set", mode)
		}
		if cr.accuracy != 5*time.Second {
			t.Errorf("%s: accuracy = %v, want 5s", mode, cr.accuracy)
		}
	}

	interval := NewProcessService(set, "mods-interval")
	interval.SetCronConfig([]string{"/bin/true"}, time.Hour, 0, "continue")
	check("interval", interval)

	cal := NewProcessService(set, "mods-calendar")
	spec, err := ParseCalendar("daily")
	if err != nil {
		t.Fatal(err)
	}
	cal.SetCronCalendar([]string{"/bin/true"}, spec, 0, false, "continue")
	check("calendar", cal)

	// No cron configured at all must stay a no-op rather than panic.
	NewProcessService(set, "mods-none").SetCronModifiers(time.Minute, true, true, time.Second)
}
