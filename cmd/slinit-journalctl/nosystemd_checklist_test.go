// Regression tests derived from the systemd bug list on nosystemd.org.
// See pkg/journal/nosystemd_checklist_test.go for the rationale.
package main

import (
	"fmt"
	"strings"
	"testing"

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
