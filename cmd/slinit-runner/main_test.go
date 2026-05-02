package main

import (
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
)

func TestParseNodeListSingles(t *testing.T) {
	got, err := parseNodeList("0,2,4")
	if err != nil {
		t.Fatalf("parseNodeList: %v", err)
	}
	want := []uint{0, 2, 4}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseNodeListRange(t *testing.T) {
	got, err := parseNodeList("0-3")
	if err != nil {
		t.Fatalf("parseNodeList: %v", err)
	}
	want := []uint{0, 1, 2, 3}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseNodeListMixed(t *testing.T) {
	got, err := parseNodeList("0-1,3,5-6")
	if err != nil {
		t.Fatalf("parseNodeList: %v", err)
	}
	want := []uint{0, 1, 3, 5, 6}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseNodeListDedup(t *testing.T) {
	got, err := parseNodeList("0,0,1-2,2")
	if err != nil {
		t.Fatalf("parseNodeList: %v", err)
	}
	want := []uint{0, 1, 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseNodeListInvalid(t *testing.T) {
	for _, s := range []string{"abc", "5-2", "1-", "-3", "1,abc"} {
		if _, err := parseNodeList(s); err == nil {
			t.Errorf("parseNodeList(%q): expected error", s)
		}
	}
}

func TestParseMempolicy(t *testing.T) {
	cases := []struct {
		mode      string
		nodes     string
		wantMode  uint32
		wantNodes []uint
		wantErr   bool
	}{
		{"default", "", unix.MPOL_DEFAULT, nil, false},
		{"local", "", unix.MPOL_LOCAL, nil, false},
		{"bind", "0,1", unix.MPOL_BIND, []uint{0, 1}, false},
		{"interleave", "0-3", unix.MPOL_INTERLEAVE, []uint{0, 1, 2, 3}, false},
		{"preferred", "2", unix.MPOL_PREFERRED, []uint{2}, false},

		// Negative cases: required nodes missing, or extraneous nodes.
		{"bind", "", 0, nil, true},
		{"interleave", "", 0, nil, true},
		{"local", "0", 0, nil, true},
		{"default", "0", 0, nil, true},
		{"unknown", "", 0, nil, true},
	}
	for _, c := range cases {
		gotMode, gotNodes, err := parseMempolicy(c.mode, c.nodes)
		if (err != nil) != c.wantErr {
			t.Errorf("parseMempolicy(%q, %q): err=%v wantErr=%v", c.mode, c.nodes, err, c.wantErr)
			continue
		}
		if c.wantErr {
			continue
		}
		if gotMode != c.wantMode {
			t.Errorf("parseMempolicy(%q): mode=%d want %d", c.mode, gotMode, c.wantMode)
		}
		if !reflect.DeepEqual(gotNodes, c.wantNodes) {
			t.Errorf("parseMempolicy(%q): nodes=%v want %v", c.mode, gotNodes, c.wantNodes)
		}
	}
}
