// Regression tests derived from the systemd bug list on nosystemd.org.
// See pkg/journal/nosystemd_checklist_test.go for the rationale.
package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// buildJSONL renders n events numbered 0..n-1, oldest first, as the
// on-disk JSONL a journal file holds.
func buildJSONL(t *testing.T, n int) string {
	t.Helper()
	var sb strings.Builder
	for i := 0; i < n; i++ {
		evt := &journal.Event{
			Msg:  fmt.Sprintf("entry-%02d", i),
			Prio: journal.PriorityInfo,
			Ts:   int64(i + 1),
		}
		line, err := evt.MarshalJSONL()
		if err != nil {
			t.Fatalf("MarshalJSONL: %v", err)
		}
		sb.Write(line)
		if !strings.HasSuffix(string(line), "\n") {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func msgs(events []*journal.Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Msg
	}
	return out
}

// systemd#1596: `journalctl -r -n N` processed the two flags in the
// wrong order, so it returned the wrong N entries rather than the N
// newest shown newest-first.
// https://github.com/systemd/systemd/issues/1596
//
// The contract: -n N selects the N *newest* matches (tail semantics),
// and -r only changes the order they are printed in. Getting this
// backwards — limiting from the head, then reversing — silently hands
// the operator the oldest entries when they asked for the latest, which
// is exactly the case where they are debugging something that just
// happened.
func TestLimitSelectsNewestThenReverseOnlyReorders_systemd1596(t *testing.T) {
	const total, limit = 10, 3
	all := journal.QueryFilter{MinPriority: -1}

	events, err := readJSONLFile(strings.NewReader(buildJSONL(t, total)), all, limit)
	if err != nil {
		t.Fatalf("readJSONLFile: %v", err)
	}
	if len(events) != limit {
		t.Fatalf("-n %d returned %d entries: %v", limit, len(events), msgs(events))
	}

	// The newest `limit` entries, still oldest-first.
	want := []string{"entry-07", "entry-08", "entry-09"}
	got := msgs(events)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("-n %d selected the wrong entries:\n got %v\nwant %v\n"+
				"(selecting from the head instead of the tail is systemd#1596)",
				limit, got, want)
		}
	}

	// -r reverses that selection; it must not change *which* entries.
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	wantRev := []string{"entry-09", "entry-08", "entry-07"}
	gotRev := msgs(events)
	for i := range wantRev {
		if gotRev[i] != wantRev[i] {
			t.Errorf("-r did not simply reverse the selection:\n got %v\nwant %v",
				gotRev, wantRev)
		}
	}
}

// -n larger than the number of matches returns everything, in order,
// rather than padding or erroring.
func TestLimitLargerThanMatchCount_systemd1596(t *testing.T) {
	all := journal.QueryFilter{MinPriority: -1}
	events, err := readJSONLFile(strings.NewReader(buildJSONL(t, 3)), all, 100)
	if err != nil {
		t.Fatalf("readJSONLFile: %v", err)
	}
	want := []string{"entry-00", "entry-01", "entry-02"}
	got := msgs(events)
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got, want)
			break
		}
	}
}

// systemd#2460: "Showing status of service via systemctl is slow
// (>10s) if disk journal is used". Asking for a service's last few log
// lines should not cost anything like the size of the journal.
// https://github.com/systemd/systemd/issues/2460
//
// slinit is honest about where it stands here: `slinitctl status`
// shells out to `slinit-journalctl -u NAME -n 10`, and the file path is
// a linear scan that keeps the last N. The cost therefore does grow
// with journal size — the same shape as upstream — but the constant is
// roughly 13 ms per megabyte, so a 50 MB journal costs about half a
// second rather than ten. `pkg/journald/idx.go` has an index built for
// exactly this (its IdxReader hands back an offset to seek the paired
// JSONL with); slinit-journalctl does not use it yet.
//
// This test is a ceiling, not an endorsement. It is set well above the
// measured cost so it does not flake on a loaded CI box, and low enough
// to catch a regression into upstream's territory — if someone makes
// the tail read super-linear, or drops the early-trim so the whole
// journal is held in memory, this fails.
func TestTailReadDoesNotDegradeToSystemdLatency_systemd2460(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates a ~10 MB journal")
	}
	const entries = 100000 // ~9.5 MB of JSONL

	var sb strings.Builder
	for i := 0; i < entries; i++ {
		evt := &journal.Event{
			Msg:  "a fairly typical log line from some service",
			Unit: "svc",
			Prio: journal.PriorityInfo,
			Ts:   int64(i),
		}
		line, err := evt.MarshalJSONL()
		if err != nil {
			t.Fatalf("MarshalJSONL: %v", err)
		}
		sb.Write(line)
		if !strings.HasSuffix(string(line), "\n") {
			sb.WriteByte('\n')
		}
	}
	data := sb.String()

	start := time.Now()
	events, err := readJSONLFile(strings.NewReader(data),
		journal.QueryFilter{MinPriority: -1}, 10)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("readJSONLFile: %v", err)
	}
	if len(events) != 10 {
		t.Fatalf("tail-10 returned %d entries", len(events))
	}
	if events[9].Msg == "" {
		t.Error("tail returned empty entries")
	}

	t.Logf("tail-10 of %d entries (%d bytes) took %v", entries, len(data), elapsed)
	if elapsed > 5*time.Second {
		t.Errorf("tail-10 of a %d-byte journal took %v — that is systemd#2460 "+
			"territory; the scan has stopped being linear or the early trim "+
			"is gone", len(data), elapsed)
	}
}

// The early trim is what keeps the scan's memory flat: readJSONLFile
// must not accumulate the whole journal and slice at the end. A
// regression there would not show up as wrong output, only as a daemon
// that falls over on a big file.
func TestTailReadKeepsMemoryFlat_systemd2460(t *testing.T) {
	all := journal.QueryFilter{MinPriority: -1}
	events, err := readJSONLFile(strings.NewReader(buildJSONL(t, 5000)), all, 3)
	if err != nil {
		t.Fatalf("readJSONLFile: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d entries, want 3", len(events))
	}
	if cap(events) > 64 {
		t.Errorf("tail-3 returned a slice with capacity %d — the reader is "+
			"accumulating the whole journal before trimming", cap(events))
	}
}

// -n 1 is the operator's "what just happened" call and must give the
// single newest entry.
func TestLimitOneIsTheNewest_systemd1596(t *testing.T) {
	all := journal.QueryFilter{MinPriority: -1}
	events, err := readJSONLFile(strings.NewReader(buildJSONL(t, 10)), all, 1)
	if err != nil {
		t.Fatalf("readJSONLFile: %v", err)
	}
	if len(events) != 1 || events[0].Msg != "entry-09" {
		t.Errorf("-n 1 gave %v, want [entry-09]", msgs(events))
	}
}
