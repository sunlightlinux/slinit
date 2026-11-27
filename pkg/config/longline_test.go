package config

import (
	"strings"
	"testing"
)

// A line longer than the scanner's limit (64 KiB) failed the load with
// "reading service description for svc: bufio.Scanner: token too long":
// no file name, which matters when several service directories are
// searched, and no line. It is now a ParseError like any other error in
// the file.
func TestParseLineTooLongNamesFileAndLine(t *testing.T) {
	in := "type = internal\n# fine\n" + strings.Repeat("x", 70*1024) + "\n"
	_, err := Parse(strings.NewReader(in), "svc", "/etc/slinit.d/svc")
	if err == nil {
		t.Fatal("parsed a 70 KiB line without error")
	}
	pe, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("error %T %q, want *ParseError", err, err)
	}
	if pe.Line != 3 {
		t.Errorf("line %d, want 3", pe.Line)
	}
	if msg := err.Error(); !strings.Contains(msg, "/etc/slinit.d/svc:3:") || !strings.Contains(msg, "too long") {
		t.Errorf("message %q lacks the file:line or the reason", msg)
	}
}
