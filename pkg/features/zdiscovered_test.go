package features

import (
	"bytes"
	"os"
	"testing"
)

// TestBuiltinListsCurrent regenerates zdiscovered.go in memory from
// the parser and protocol sources and fails when the committed copy
// differs: a directive or opcode added without `go generate
// ./pkg/features` would otherwise be missing from slinit-supports.
func TestBuiltinListsCurrent(t *testing.T) {
	opcodes, directives, err := DiscoverAll("../..")
	if err != nil {
		t.Fatal(err)
	}
	want, err := RenderDiscovered(opcodes, directives)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("zdiscovered.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale (%d opcodes, %d directives in source; compiled-in %d, %d); run `go generate ./pkg/features`",
			GeneratedFile, len(opcodes), len(directives), len(BuiltinOpcodes), len(BuiltinDirectives))
	}
}
