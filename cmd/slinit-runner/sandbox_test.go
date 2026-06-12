package main

import (
	"reflect"
	"testing"
)

// TestStringListAppend verifies the flag.Value adapter accumulates
// repeated --read-only-path / --read-write-path occurrences in order.
func TestStringListAppend(t *testing.T) {
	var sl stringList
	for _, v := range []string{"/a", "/b", "/c"} {
		if err := sl.Set(v); err != nil {
			t.Fatalf("Set(%q): %v", v, err)
		}
	}
	if !reflect.DeepEqual([]string(sl), []string{"/a", "/b", "/c"}) {
		t.Errorf("got %v, want [/a /b /c]", sl)
	}
	if got, want := sl.String(), "/a,/b,/c"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

// TestStringListRejectsEmpty makes sure an empty value (e.g. from a
// stray "--read-only-path=" on the command line) is rejected rather
// than silently appended.
func TestStringListRejectsEmpty(t *testing.T) {
	var sl stringList
	if err := sl.Set(""); err == nil {
		t.Error("expected error on empty value")
	}
}
