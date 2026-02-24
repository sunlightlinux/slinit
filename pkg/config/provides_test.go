package config

import (
	"strings"
	"testing"
)

func TestProvidesParsing(t *testing.T) {
	input := `
type = process
command = /bin/sleep 60
provides = my-alias
`
	desc, err := Parse(strings.NewReader(input), "real-svc", "test-file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desc.Provides != "my-alias" {
		t.Errorf("expected Provides='my-alias', got '%s'", desc.Provides)
	}
}

func TestProvidesEmpty(t *testing.T) {
	input := `
type = process
command = /bin/sleep 60
`
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desc.Provides != "" {
		t.Errorf("expected empty Provides, got '%s'", desc.Provides)
	}
}
