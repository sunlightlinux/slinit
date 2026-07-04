package main

import (
	"strings"
	"testing"
)

func TestParseLineHappyPath(t *testing.T) {
	cases := []struct {
		in       string
		wantKey  string
		wantVal  string
		wantIgn  bool
	}{
		{"net.ipv4.ip_forward = 1", "net/ipv4/ip_forward", "1", false},
		{"kernel.printk = 4 4 1 7", "kernel/printk", "4 4 1 7", false},
		{"net/ipv4/tcp_syncookies=1", "net/ipv4/tcp_syncookies", "1", false},
		{"-vm.swappiness = 60", "vm/swappiness", "60", true},
		{"  fs.file-max  =  100000  ", "fs/file-max", "100000", false},
		{"kernel.hostname = my.host.example", "kernel/hostname", "my.host.example", false},
	}
	for _, tc := range cases {
		got, err := parseLine(tc.in)
		if err != nil {
			t.Errorf("parseLine(%q): %v", tc.in, err)
			continue
		}
		if got.key != tc.wantKey {
			t.Errorf("parseLine(%q).key = %q, want %q", tc.in, got.key, tc.wantKey)
		}
		if got.value != tc.wantVal {
			t.Errorf("parseLine(%q).value = %q, want %q", tc.in, got.value, tc.wantVal)
		}
		if got.ignoreErrors != tc.wantIgn {
			t.Errorf("parseLine(%q).ignoreErrors = %v, want %v",
				tc.in, got.ignoreErrors, tc.wantIgn)
		}
	}
}

func TestParseLineRejectsBad(t *testing.T) {
	bad := []string{
		"",               // empty
		"no equals",      // missing '='
		"= 1",            // empty key
		"- = 1",          // key is only the dash prefix
		"net.*.forwarding = 1", // wildcard
		"/net.ipv4 = 1",  // leading slash
		"net..ipv4 = 1",  // consecutive dots produce '//' after normalization
	}
	for _, in := range bad {
		if _, err := parseLine(in); err == nil {
			t.Errorf("parseLine(%q): expected error", in)
		}
	}
}

func TestParseFileSkipsBlankAndComments(t *testing.T) {
	body := `# comment
; also a comment
   # indented comment

net.ipv4.ip_forward = 1
-vm.swappiness = 60
`
	specs, err := parseFile(strings.NewReader(body), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 {
		t.Fatalf("len=%d, want 2", len(specs))
	}
	if specs[0].key != "net/ipv4/ip_forward" || specs[0].value != "1" {
		t.Errorf("spec 0: %+v", specs[0])
	}
	if specs[1].key != "vm/swappiness" || !specs[1].ignoreErrors {
		t.Errorf("spec 1: %+v", specs[1])
	}
	// Line numbers preserved for diagnostics.
	if specs[0].sourceLineNo != 5 || specs[1].sourceLineNo != 6 {
		t.Errorf("line numbers: %d,%d", specs[0].sourceLineNo, specs[1].sourceLineNo)
	}
}

func TestParseFilePropagatesLineNoInError(t *testing.T) {
	body := "net.core.somaxconn = 1024\ninvalid line without equals\n"
	_, err := parseFile(strings.NewReader(body), "fx")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "fx:2") {
		t.Errorf("err missing 'fx:2': %v", err)
	}
}
