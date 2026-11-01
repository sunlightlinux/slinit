package process

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
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

func TestParseNotifyMessageFDStoreRemoval(t *testing.T) {
	m := ParseNotifyMessage([]byte("FDSTOREREMOVE=1\nFDNAME=old\n"))
	if !m.FDStoreRemove || m.FDName != "old" || m.HasFDStoreWipe {
		t.Errorf("FDSTOREREMOVE=1: got %+v", m)
	}
	m = ParseNotifyMessage([]byte("FDSTOREWIPE=conn-*\n"))
	if !m.HasFDStoreWipe || m.FDStoreWipe != "conn-*" {
		t.Errorf("FDSTOREWIPE=conn-*: got %+v", m)
	}
	// An empty pattern is present but invalid; OnNotify must be able to
	// tell it from no FDSTOREWIPE= at all.
	m = ParseNotifyMessage([]byte("FDSTOREWIPE=\n"))
	if !m.HasFDStoreWipe || m.FDStoreWipe != "" {
		t.Errorf("FDSTOREWIPE= (empty): got %+v", m)
	}
}

// The cases sd_notify(3) spells out for FDSTOREWIPE=, which follows
// fnmatch(3) with no flags, plus the set syntax fnmatch has.
func TestMatchFDName(t *testing.T) {
	for _, c := range []struct {
		pattern, name string
		want          bool
	}{
		{"*", "anything", true},
		{"*", "", true},        // matches empty names
		{"*", ".hidden", true}, // and a leading dot
		{"*", "a/b", true},     // and slashes: no FNM_PATHNAME
		{"conn-*", "conn-12", true},
		{"conn-*", "conn", false},
		{"listener", "listener", true}, // no wildcard: exact name only
		{"listener", "listener2", false},
		{"Listener", "listener", false}, // case-sensitive
		{"a?c", "abc", true},
		{"a?c", "a/c", true},
		{"a?c", "ac", false},
		{"[ab]x", "bx", true},
		{"[ab]x", "cx", false},
		{"[!ab]x", "cx", true},
		{"[^ab]x", "ax", false},
		{"[a-c]", "b", true},
		{"[a-c]", "d", false},
		{"[]]", "]", true}, // ']' first in a set is a member
		{`\*`, "*", true},  // escaped wildcard is literal
		{`\*`, "x", false},
		{"[ab", "[ab", true}, // unclosed '[' is literal
		{"[ab", "a", false},
		{"*a*b*", "xxaYYbzz", true},
		{"*a*b*", "xxbYYazz", false},
	} {
		if got := MatchFDName(c.pattern, c.name); got != c.want {
			t.Errorf("MatchFDName(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

func TestValidFDName(t *testing.T) {
	for _, ok := range []string{"stored", "conn-*", "a b", strings.Repeat("x", 255)} {
		if !ValidFDName(ok) {
			t.Errorf("ValidFDName(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "a:b", "tab\there", "nl\n", "\x7f", strings.Repeat("x", 256)} {
		if ValidFDName(bad) {
			t.Errorf("ValidFDName(%q) = true, want false", bad)
		}
	}
}

func TestFDStoreRemoveClosesWhatItDrops(t *testing.T) {
	s := NewFDStore(8)
	files := map[string]*os.File{}
	for _, name := range []string{"conn-1", "listener", "conn-2"} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()
		files[name] = r
		if err := s.Add(FDStoreEntry{Name: name, File: r}); err != nil {
			t.Fatal(err)
		}
	}
	if n := s.Remove(func(name string) bool { return MatchFDName("conn-*", name) }); n != 2 {
		t.Fatalf("Remove(conn-*) dropped %d, want 2", n)
	}
	if s.Len() != 1 {
		t.Fatalf("store has %d entries after the wipe, want 1", s.Len())
	}
	for name, f := range files {
		closed := errors.Is(f.Close(), os.ErrClosed)
		if wantClosed := name != "listener"; closed != wantClosed {
			t.Errorf("%s: closed by Remove = %v, want %v", name, closed, wantClosed)
		}
	}
	if left := s.Drain(); len(left) != 1 || left[0].Name != "listener" {
		t.Errorf("left in the store: %v, want only listener", left)
	}
}

// A service picks the pattern and PID 1 runs the match: a backtracking
// '*' takes exponential time on this, the linear form does not.
func TestMatchFDNameIsLinear(t *testing.T) {
	pattern := strings.Repeat("*a", 120) + "*b"
	name := strings.Repeat("a", 255)
	start := time.Now()
	if MatchFDName(pattern, name) {
		t.Fatal("matched a name with no 'b'")
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("worst-case match took %v; the matcher backtracks", d)
	}
}

// matchFDNameRef is the obvious recursive matcher, kept only as the
// reference the linear one is fuzzed against.
func matchFDNameRef(pattern, name string) bool {
	if pattern == "" {
		return name == ""
	}
	if pattern[0] == '*' {
		// "**" is "*"; collapsing keeps the reference usable, since a
		// run of stars is exactly what makes it exponential.
		pattern = strings.TrimLeft(pattern, "*")
		for i := 0; i <= len(name); i++ {
			if matchFDNameRef(pattern, name[i:]) {
				return true
			}
		}
		return false
	}
	if name == "" {
		return false
	}
	next, ok := matchFDNameOne(pattern, 0, name[0])
	return ok && matchFDNameRef(pattern[next:], name[1:])
}

func FuzzMatchFDName(f *testing.F) {
	for _, s := range [][2]string{
		{"*", ""}, {"conn-*", "conn-1"}, {"[!a-c]?*x", "dzzx"}, {`\*[]]*`, "*]q"}, {"[ab", "[ab"}, {"*a*b", "xaxb"},
	} {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, pattern, name string) {
		if len(pattern) > 16 || len(name) > 16 {
			return // keep the exponential reference fast
		}
		if got, want := MatchFDName(pattern, name), matchFDNameRef(pattern, name); got != want {
			t.Fatalf("MatchFDName(%q, %q) = %v, reference says %v", pattern, name, got, want)
		}
	})
}
