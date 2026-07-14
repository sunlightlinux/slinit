package config

import (
	"strings"
	"testing"
)

func TestParseMemoryTHP(t *testing.T) {
	for _, mode := range []string{"never", "madvise", "always"} {
		input := "type = process\ncommand = /bin/true\nmemory-thp = " + mode + "\n"
		desc, err := Parse(strings.NewReader(input), "svc", "test")
		if err != nil {
			t.Errorf("%q: parse: %v", mode, err)
			continue
		}
		if desc.MemoryTHP != mode {
			t.Errorf("%q: got %q", mode, desc.MemoryTHP)
		}
	}
}

func TestParseMemoryTHPRejectsUnknown(t *testing.T) {
	input := "type = process\ncommand = /bin/true\nmemory-thp = maybe\n"
	if _, err := Parse(strings.NewReader(input), "svc", "test"); err == nil {
		t.Fatal("expected error for unknown memory-thp value")
	}
}

func TestParseFDStorePreserve(t *testing.T) {
	for _, v := range []string{"no", "yes", "on-success"} {
		input := "type = process\ncommand = /bin/true\n" +
			"file-descriptor-store-max = 4\n" +
			"file-descriptor-store-preserve = " + v + "\n"
		desc, err := Parse(strings.NewReader(input), "svc", "test")
		if err != nil {
			t.Errorf("%q: parse: %v", v, err)
			continue
		}
		if desc.FileDescriptorStorePreserve != v {
			t.Errorf("%q: got %q", v, desc.FileDescriptorStorePreserve)
		}
	}
}

func TestParseFDStorePreserveRejectsUnknown(t *testing.T) {
	input := "type = process\ncommand = /bin/true\n" +
		"file-descriptor-store-preserve = sometimes\n"
	if _, err := Parse(strings.NewReader(input), "svc", "test"); err == nil {
		t.Fatal("expected error for unknown file-descriptor-store-preserve value")
	}
}

// TestParsePathIsSocketPredicate pins the new predicate name at the
// config surface — makes sure the parser routes it to the right kind.
func TestParsePathIsSocketPredicate(t *testing.T) {
	input := "type = process\ncommand = /bin/true\n" +
		"condition-path-is-socket = /run/foo.sock\n"
	desc, err := Parse(strings.NewReader(input), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(desc.Predicates) != 1 {
		t.Fatalf("predicates: got %d want 1", len(desc.Predicates))
	}
	// Sanity: param made it through, kind is the socket kind.
	p := desc.Predicates[0]
	if p.Param != "/run/foo.sock" {
		t.Errorf("param: got %q", p.Param)
	}
}

func TestParseFractionPredicate(t *testing.T) {
	input := "type = process\ncommand = /bin/true\n" +
		"condition-fraction = my-rollout:25\n"
	desc, err := Parse(strings.NewReader(input), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(desc.Predicates) != 1 {
		t.Fatalf("predicates: got %d want 1", len(desc.Predicates))
	}
	p := desc.Predicates[0]
	if p.Param != "my-rollout:25" {
		t.Errorf("param: got %q", p.Param)
	}
}
