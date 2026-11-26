package main

import (
	"strings"
	"testing"
)

func TestParseNameStandard(t *testing.T) {
	cases := map[string]string{
		":qemu-x86_64:M::\\x7fELF:...":                 "qemu-x86_64",
		":wsl-interop:M::MZ:\\xff\\xff:/usr/bin/wsl:P": "wsl-interop",
		":mono:E::exe::/usr/bin/mono:":                 "mono",
		"|other|M::AAA::/bin/x:":                       "other",
		":aa:M::x::y:":                                 "aa",
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

// A spec named after binfmt_misc's own control files must be refused.
// registerSpec first unregisters an existing entry of the same name by
// writing -1 to binfmt_misc/<name>, and binfmt_misc/status takes -1 as
// "remove every registered format": one stray `:status:...` line in a
// binfmt.d file wiped all handlers (qemu-user, Wine, ...) at boot.
// "." and ".." name the directory and its parent; the kernel refuses
// them as names too.
func TestParseNameRejectsReservedNames(t *testing.T) {
	for _, name := range []string{"status", "register", ".", ".."} {
		in := ":" + name + ":M::\\x7fELF::/usr/bin/qemu:F"
		if _, err := parseName(in); err == nil {
			t.Errorf("parseName(%q): accepted the reserved name %q", in, name)
		}
	}
}

// Trailing blanks and a CR are stripped, as systemd-binfmt's
// read_stripped_line does: the kernel reads them as part of the flags
// field and rejects the whole rule with EINVAL.
func TestParseFileStripsTrailingWhitespace(t *testing.T) {
	body := ":foo:E::foo::/bin/true: \t\n:bar:E::bar::/bin/true:F\r\n"
	specs, err := parseFile(strings.NewReader(body), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 {
		t.Fatalf("len=%d, want 2", len(specs))
	}
	if want := ":foo:E::foo::/bin/true:"; specs[0].line != want {
		t.Errorf("line[0]=%q, want %q", specs[0].line, want)
	}
	if want := ":bar:E::bar::/bin/true:F"; specs[1].line != want {
		t.Errorf("line[1]=%q, want %q", specs[1].line, want)
	}
}
