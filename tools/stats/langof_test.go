package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLangOfCAndCPlusPlus(t *testing.T) {
	for path, want := range map[string]string{
		"lib/slinit-mlock/slinit-mlock.c": "C",
		"x/y.h":                           "C",
		"a.cc":                            "C++",
		"a.cpp":                           "C++",
		"a.hpp":                           "C++",
		"a.go":                            "Go",
		"a.txt":                           "",
	} {
		if got := langOf(path); got != want {
			t.Errorf("langOf(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestCountLinesC(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.c")
	src := "/*\n * header\n */\n#include <x.h>\n\n// note\nint main(void) { return 0; }\n"
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := countLines(p, "C")
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 7 || got.Comments != 4 || got.Code != 2 || got.Blank != 1 {
		t.Errorf("got %+v, want total 7, comments 4, code 2, blank 1", got)
	}
}
