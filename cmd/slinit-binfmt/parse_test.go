package main

import (
	"strings"
	"testing"
)

func TestParseNameStandard(t *testing.T) {
	cases := map[string]string{
		":qemu-x86_64:M::\\x7fELF:...":                        "qemu-x86_64",
		":wsl-interop:M::MZ:\\xff\\xff:/usr/bin/wsl:P":        "wsl-interop",
		":mono:E::exe::/usr/bin/mono:":                        "mono",
		"|other|M::AAA::/bin/x:":                              "other",
		":aa:M::x::y:":                                        "aa",
	}
	for in, want := range cases {
		got, err := parseName(in)
		if err != nil {
			t.Errorf("parseName(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseNameRejectsBad(t *testing.T) {
	bad := []string{
		"",         // empty
		":",        // no name
		"::",       // empty name
		"aaa",      // no delimiter at start (delimiter would be 'a' — alnum)
		"1x:",      // digit delimiter
		":a/b:M::", // slash in name
	}
	for _, in := range bad {
		if _, err := parseName(in); err == nil {
			t.Errorf("parseName(%q): expected error", in)
		}
	}
}

func TestParseFileSkipsBlankAndComments(t *testing.T) {
	body := `# a comment

; also a comment
   # indented comment

:foo:M::AA:FF:/bin/foo:
:bar:E::exe::/bin/bar:
`
	specs, err := parseFile(strings.NewReader(body), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 {
		t.Fatalf("len=%d, want 2", len(specs))
	}
	if specs[0].name != "foo" || specs[1].name != "bar" {
		t.Errorf("names=%q,%q", specs[0].name, specs[1].name)
	}
	if !strings.HasPrefix(specs[0].line, ":foo:") {
		t.Errorf("line[0]=%q", specs[0].line)
	}
}

func TestParseFilePropagatesLineNo(t *testing.T) {
	body := "\n\n:good:M::A::/bin/good:\n:bad_delim_alnum\n"
	_, err := parseFile(strings.NewReader(body), "fixture")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "fixture:4") {
		t.Errorf("err missing line 4: %v", err)
	}
}
