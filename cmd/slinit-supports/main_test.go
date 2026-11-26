package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/features"
)

// An installed slinit-supports has no source tree around it. It used
// to discover directives from ./pkg/config/parser.go relative to the
// working directory, so `cd / && slinit-supports capabilities` said
// "No" (exit 1) where the same build in the repo said "Yes", and
// --list-all printed 198 lines instead of 387. Run from an empty
// directory and compare with discovery against the real source.
func TestAnswersDoNotDependOnWorkingDirectory(t *testing.T) {
	opcodes, directives, err := features.DiscoverAll("../..")
	if err != nil {
		t.Fatal(err)
	}
	fromSource, _ := features.Load(opcodes, directives)
	var want bytes.Buffer
	if err := run(&want, fromSource, options{listMode: "all", format: "text"}); err != nil {
		t.Fatal(err)
	}

	t.Chdir(t.TempDir())
	reg, _ := loadRegistry()

	var out bytes.Buffer
	if err := run(&out, reg, options{name: "capabilities", format: "text"}); err != nil {
		t.Fatalf("capabilities from an empty cwd: %v\n%s", err, out.String())
	}
	if !strings.HasPrefix(out.String(), "Yes  capabilities (directive)") {
		t.Fatalf("capabilities from an empty cwd:\n%s", out.String())
	}

	var got bytes.Buffer
	if err := run(&got, reg, options{listMode: "all", format: "text"}); err != nil {
		t.Fatal(err)
	}
	gl, wl := strings.Count(got.String(), "\n"), strings.Count(want.String(), "\n")
	if gl != wl {
		t.Fatalf("--list-all from an empty cwd: %d lines, from source %d", gl, wl)
	}
	if got.String() != want.String() {
		t.Fatal("--list-all from an empty cwd differs from discovery against the source")
	}
}
