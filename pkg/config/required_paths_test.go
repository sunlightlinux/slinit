package config

import (
	"strings"
	"testing"
)

// TestParseRequiredFilesSpaceSeparated covers the OpenRC shell-array idiom
// where multiple paths live on one line, separated by whitespace.
func TestParseRequiredFilesSpaceSeparated(t *testing.T) {
	input := `type = process
command = /bin/true
required-files = /etc/foo.conf /etc/bar.conf
`
	desc, err := Parse(strings.NewReader(input), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"/etc/foo.conf", "/etc/bar.conf"}
	if len(desc.RequiredFiles) != len(want) {
		t.Fatalf("got %d entries, want %d: %v", len(desc.RequiredFiles), len(want), desc.RequiredFiles)
	}
	for i, p := range want {
		if desc.RequiredFiles[i] != p {
			t.Errorf("entry %d: got %q, want %q", i, desc.RequiredFiles[i], p)
		}
	}
}

// TestParseRequiredFilesMultiline covers the slinit-native one-per-line idiom,
// which also works because the parser calls strings.Fields on each value.
func TestParseRequiredFilesMultiline(t *testing.T) {
	input := `type = process
command = /bin/true
required-files = /etc/foo.conf
required-files += /etc/bar.conf
`
	desc, err := Parse(strings.NewReader(input), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(desc.RequiredFiles) != 2 {
		t.Fatalf("got %v, want 2 entries", desc.RequiredFiles)
	}
	if desc.RequiredFiles[0] != "/etc/foo.conf" || desc.RequiredFiles[1] != "/etc/bar.conf" {
		t.Errorf("unexpected entries: %v", desc.RequiredFiles)
	}
}

// TestParseRequiredDirsSpaceSeparated mirrors the files test for directories.
func TestParseRequiredDirsSpaceSeparated(t *testing.T) {
	input := `type = process
command = /bin/true
required-dirs = /var/run/myapp /var/log/myapp
`
	desc, err := Parse(strings.NewReader(input), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(desc.RequiredDirs) != 2 {
		t.Fatalf("got %v, want 2 entries", desc.RequiredDirs)
	}
}
