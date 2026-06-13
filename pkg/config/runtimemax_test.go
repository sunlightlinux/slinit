package config

import (
	"strings"
	"testing"
	"time"
)

func TestParseRuntimeMaxSec(t *testing.T) {
	input := "type = process\ncommand = /bin/true\nruntime-max-sec = 30s\n"
	desc, err := Parse(strings.NewReader(input), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.RuntimeMaxSec != 30*time.Second {
		t.Errorf("RuntimeMaxSec: got %v want 30s", desc.RuntimeMaxSec)
	}
}

func TestParseRuntimeMaxSecBadValue(t *testing.T) {
	input := "type = process\ncommand = /bin/true\nruntime-max-sec = nope\n"
	if _, err := Parse(strings.NewReader(input), "svc", "test"); err == nil {
		t.Fatal("expected error for malformed duration")
	}
}
