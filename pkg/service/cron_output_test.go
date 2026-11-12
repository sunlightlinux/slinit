package service

import (
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// cronJournal installs a global emitter for the test and returns a function
// listing the events of one unit.
func cronJournal(t *testing.T) func(unit string) []*journal.Event {
	t.Helper()
	if err := journal.InitIDs("testhost"); err != nil {
		t.Fatal(err)
	}
	buf := journal.NewEventBuffer(1024)
	journal.SetGlobal(journal.NewEmitter(buf, ""))
	t.Cleanup(func() { journal.SetGlobal(nil) })
	return func(unit string) []*journal.Event {
		all, _ := buf.Snapshot()
		var out []*journal.Event
		for _, e := range all {
			if e.Unit == unit {
				out = append(out, e)
			}
		}
		return out
	}
}

func cronRunOnce(t *testing.T, unit, script string) error {
	t.Helper()
	set, l := newTestSet()
	cr := NewCronRunner(NewInternalService(set, unit), []string{"/bin/sh", "-c", script}, time.Minute, 0, "", l)
	cr.stopCh = make(chan struct{})
	return cr.executeCommand()
}

// The output of a cron command used to go to /dev/null: a nightly update left
// no record of what it did.
func TestCronOutputReachesJournal(t *testing.T) {
	events := cronJournal(t)
	err := cronRunOnce(t, "cronout", `echo first; echo "ERROR: second" >&2; printf 'no newline'; exit 3`)
	if err == nil {
		t.Fatal("exit 3 reported as success")
	}
	got := events("cronout")
	var lines []string
	for _, e := range got {
		if e.Transport == journal.TransportStdout {
			lines = append(lines, e.Msg)
		}
	}
	if want := []string{"first", "ERROR: second", "no newline"}; strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("output lines = %q, want %q", lines, want)
	}
	for _, e := range got {
		if e.Msg == "ERROR: second" && e.Prio != journal.PriorityError {
			t.Errorf("ERROR: line has priority %v, want err", e.Prio)
		}
	}
	last := got[len(got)-1]
	if last.Transport != journal.TransportDriver || last.Fields["SLINIT_CRON_RESULT"] != "exit-code" ||
		last.Fields["SLINIT_EXIT_STATUS"] != "3" || last.Prio != journal.PriorityError {
		t.Fatalf("last event = %+v, want the exit-status-3 summary", last)
	}
}

func TestCronRunSummaryOnSuccess(t *testing.T) {
	events := cronJournal(t)
	if err := cronRunOnce(t, "cronok", "true"); err != nil {
		t.Fatal(err)
	}
	got := events("cronok")
	if len(got) != 1 || got[0].Fields["SLINIT_CRON_RESULT"] != "success" || got[0].Prio != journal.PriorityInfo {
		t.Fatalf("events = %+v, want one success summary", got)
	}
}

// A missing binary never starts: it still gets a summary, as an error.
func TestCronRunSummaryOnMissingBinary(t *testing.T) {
	events := cronJournal(t)
	set, l := newTestSet()
	cr := NewCronRunner(NewInternalService(set, "cronmissing"), []string{"/nonexistent/cron-cmd"}, time.Minute, 0, "", l)
	cr.stopCh = make(chan struct{})
	if err := cr.executeCommand(); err == nil {
		t.Fatal("missing binary reported as success")
	}
	got := events("cronmissing")
	if len(got) != 1 || got[0].Fields["SLINIT_CRON_RESULT"] != "error" {
		t.Fatalf("events = %+v, want one error summary", got)
	}
}

// A daemon the command leaves behind holds the pipe open. The run must not
// wait for it beyond the drain bound, and its later lines still arrive.
func TestCronOutputLingeringDaemon(t *testing.T) {
	events := cronJournal(t)
	orig := cronOutputDrain
	cronOutputDrain = 100 * time.Millisecond
	defer func() { cronOutputDrain = orig }()
	began := time.Now()
	if err := cronRunOnce(t, "crondaemon", `echo now; (sleep 0.5; echo later) &`); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > 400*time.Millisecond {
		t.Fatalf("run took %v: it waited for the daemon", took)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, e := range events("crondaemon") {
			if e.Msg == "later" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the daemon's line never reached the journal")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCronOutputLineCap(t *testing.T) {
	events := cronJournal(t)
	orig := cronOutputMaxLines
	cronOutputMaxLines = 5
	defer func() { cronOutputMaxLines = orig }()
	if err := cronRunOnce(t, "croncap", `i=0; while [ $i -lt 20 ]; do echo line$i; i=$((i+1)); done`); err != nil {
		t.Fatal(err)
	}
	var out, dropped int
	for _, e := range events("croncap") {
		switch {
		case e.Transport == journal.TransportStdout:
			out++
		case e.Fields["SLINIT_EVENT"] == "cron-output-dropped":
			dropped++
			if !strings.Contains(e.Msg, "15 more") {
				t.Errorf("drop note = %q, want 15 dropped", e.Msg)
			}
		}
	}
	if out != 5 || dropped != 1 {
		t.Fatalf("stdout events = %d, drop notes = %d; want 5 and 1", out, dropped)
	}
}
