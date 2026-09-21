package main

import "testing"

// withVT sets the foreground VT for one test and restores it after.
func withVT(t *testing.T, vt uint32) {
	t.Helper()
	old := currentVT.Load()
	currentVT.Store(vt)
	t.Cleanup(func() { currentVT.Store(old) })
}

func TestParseActiveVT(t *testing.T) {
	for in, want := range map[string]uint32{"tty7\n": 7, "tty1": 1, "tty12\n": 12} {
		if got, ok := parseActiveVT(in); !ok || got != want {
			t.Errorf("parseActiveVT(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "tty", "ttyS0", "tty0", "console"} {
		if _, ok := parseActiveVT(in); ok {
			t.Errorf("parseActiveVT(%q) accepted a non-VT", in)
		}
	}
}

// The scenario that left GDM's greeter blank on 2026-09-21: greeter c1
// on vt7, a Plasma session c2 on vt2. Only the session on the foreground
// VT may be active, so that switching back to vt7 is an inactive→active
// edge on c1 — the one signal gnome-shell fades its login dialog back in
// on. With every session always active there was no edge to see.
func TestSessionActivityFollowsForegroundVT(t *testing.T) {
	greeter := SessionRecord{ID: "c1", SeatID: "seat0", VTNr: 7, Class: "greeter"}
	user := SessionRecord{ID: "c2", SeatID: "seat0", VTNr: 2, Class: "user"}

	withVT(t, 2)
	if sessionIsActive(greeter) || !sessionIsActive(user) {
		t.Fatalf("on vt2: greeter active=%v user active=%v; want false, true",
			sessionIsActive(greeter), sessionIsActive(user))
	}
	if got := sessionState(greeter); got != "online" {
		t.Errorf("background session State = %q, want online", got)
	}

	currentVT.Store(7)
	if !sessionIsActive(greeter) || sessionIsActive(user) {
		t.Fatalf("back on vt7: greeter active=%v user active=%v; want true, false",
			sessionIsActive(greeter), sessionIsActive(user))
	}
	if got := sessionPropValues(greeter)["Active"]; got != true {
		t.Errorf("greeter's D-Bus Active = %v after returning to its VT", got)
	}
}

// Sessions that cannot be placed on a VT stay active: seatless ones
// (ssh) by definition, and seat0 ones without a VT number because we
// cannot prove them to be in the background. Unknown VT (containers)
// keeps everything active, the behaviour before VT tracking.
func TestSessionsWithoutAVTStayActive(t *testing.T) {
	withVT(t, 7)
	for _, rec := range []SessionRecord{
		{ID: "ssh", VTNr: 0},
		{ID: "seatless-vt", VTNr: 3},
		{ID: "novt", SeatID: "seat0", VTNr: 0},
	} {
		if !sessionIsActive(rec) {
			t.Errorf("%s reported inactive on vt7", rec.ID)
		}
	}

	currentVT.Store(0)
	if !sessionIsActive(SessionRecord{ID: "c2", SeatID: "seat0", VTNr: 2}) {
		t.Error("with the VT unknown, a seat0 session must stay active")
	}
}

func TestActiveSessionOnSeat(t *testing.T) {
	recs := []SessionRecord{
		{ID: "c1", SeatID: "seat0", VTNr: 7},
		{ID: "c2", SeatID: "seat0", VTNr: 2},
		{ID: "c3", VTNr: 0}, // ssh, no seat
	}

	withVT(t, 7)
	if got, ok := activeSessionOnSeat(recs, "seat0"); !ok || got.ID != "c1" {
		t.Errorf("vt7: active = %q, %v; want c1", got.ID, ok)
	}

	currentVT.Store(1) // a bare text console: no session there
	if got, ok := activeSessionOnSeat(recs, "seat0"); ok {
		t.Errorf("vt1 has no session, but %q was reported active", got.ID)
	}

	currentVT.Store(0) // unknown: newest session, as before VT tracking
	if got, ok := activeSessionOnSeat(recs, "seat0"); !ok || got.ID != "c2" {
		t.Errorf("unknown VT: active = %q, %v; want the newest, c2", got.ID, ok)
	}
}
