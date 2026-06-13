package process

import (
	"os"
	"testing"
)

func TestParseNotifyMessageBasic(t *testing.T) {
	body := []byte("READY=1\nSTATUS=Listening\nFDSTORE=1\nFDNAME=upstream\n")
	m := ParseNotifyMessage(body)
	if !m.Ready {
		t.Error("Ready should be true")
	}
	if m.Status != "Listening" {
		t.Errorf("Status: got %q want %q", m.Status, "Listening")
	}
	if !m.FDStore {
		t.Error("FDStore should be true")
	}
	if m.FDName != "upstream" {
		t.Errorf("FDName: got %q want %q", m.FDName, "upstream")
	}
}

func TestParseNotifyMessageIgnoresUnknownAndComments(t *testing.T) {
	body := []byte("# comment\nUNKNOWN=ignored\n\nREADY=1\n")
	m := ParseNotifyMessage(body)
	if !m.Ready {
		t.Error("Ready not parsed")
	}
}

func TestParseNotifyMessageEmpty(t *testing.T) {
	m := ParseNotifyMessage(nil)
	if m.Ready || m.FDStore || m.Watchdog || m.Stopping {
		t.Errorf("empty body: got %+v", m)
	}
}

// --- FDStore behaviour ---

func makeTempFile(t *testing.T, name string) *os.File {
	t.Helper()
	f, err := os.CreateTemp("", "fdstore-"+name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Remove(f.Name())
	})
	return f
}

func TestFDStoreAddRespectsMax(t *testing.T) {
	s := NewFDStore(2)
	if err := s.Add(FDStoreEntry{Name: "a", File: makeTempFile(t, "a")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(FDStoreEntry{Name: "b", File: makeTempFile(t, "b")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(FDStoreEntry{Name: "c", File: makeTempFile(t, "c")}); err == nil {
		t.Error("3rd Add should have been rejected (full)")
	}
	if s.Len() != 2 {
		t.Errorf("Len: got %d want 2", s.Len())
	}
}

func TestFDStoreDrainEmptiesAndReturns(t *testing.T) {
	s := NewFDStore(3)
	a := makeTempFile(t, "a")
	s.Add(FDStoreEntry{Name: "a", File: a})
	out := s.Drain()
	if len(out) != 1 || out[0].File != a {
		t.Errorf("drain: got %+v", out)
	}
	if s.Len() != 0 {
		t.Errorf("len after drain: got %d", s.Len())
	}
	// A second drain returns nothing.
	if got := s.Drain(); len(got) != 0 {
		t.Errorf("2nd drain: got %d entries", len(got))
	}
}

func TestFDStoreDisabledRejects(t *testing.T) {
	s := NewFDStore(0)
	f := makeTempFile(t, "rej")
	err := s.Add(FDStoreEntry{Name: "x", File: f})
	if err == nil {
		t.Error("Add with max=0 should error")
	}
	if s.Len() != 0 {
		t.Errorf("Len: got %d", s.Len())
	}
}
