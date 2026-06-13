package config

import (
	"strings"
	"testing"
)

func TestParseFDStoreMax(t *testing.T) {
	input := "type = process\ncommand = /bin/true\nfile-descriptor-store-max = 32\n"
	desc, err := Parse(strings.NewReader(input), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.FileDescriptorStoreMax != 32 {
		t.Errorf("got %d want 32", desc.FileDescriptorStoreMax)
	}
}

func TestParseFDStoreMaxDefault(t *testing.T) {
	input := "type = process\ncommand = /bin/true\n"
	desc, err := Parse(strings.NewReader(input), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.FileDescriptorStoreMax != 0 {
		t.Errorf("default should be 0, got %d", desc.FileDescriptorStoreMax)
	}
}

func TestParseFDStoreMaxRejectsNegative(t *testing.T) {
	input := "type = process\ncommand = /bin/true\nfile-descriptor-store-max = -3\n"
	if _, err := Parse(strings.NewReader(input), "svc", "test"); err == nil {
		t.Error("expected error for negative value")
	}
}

func TestParseFDStoreMaxRejectsNonNumeric(t *testing.T) {
	input := "type = process\ncommand = /bin/true\nfile-descriptor-store-max = many\n"
	if _, err := Parse(strings.NewReader(input), "svc", "test"); err == nil {
		t.Error("expected error for non-numeric value")
	}
}
