package config

import (
	"strings"
	"testing"
)

func TestParseLoadCredential(t *testing.T) {
	input := `
type = process
command = /bin/true
load-credential = api-key:/etc/api.key
load-credential = db-pass:/var/lib/secrets/db
`
	desc, err := Parse(strings.NewReader(input), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(desc.Credentials) != 2 {
		t.Fatalf("got %d credentials, want 2", len(desc.Credentials))
	}
	got := desc.Credentials[0]
	if got.Name != "api-key" || got.Path != "/etc/api.key" || got.Value != "" {
		t.Errorf("[0]: got %+v", got)
	}
	got = desc.Credentials[1]
	if got.Name != "db-pass" || got.Path != "/var/lib/secrets/db" || got.Value != "" {
		t.Errorf("[1]: got %+v", got)
	}
}

func TestParseSetCredential(t *testing.T) {
	input := `
type = process
command = /bin/true
set-credential = greeting:hello world
set-credential = secret:s3cr3t
`
	desc, err := Parse(strings.NewReader(input), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(desc.Credentials) != 2 {
		t.Fatalf("got %d, want 2", len(desc.Credentials))
	}
	if desc.Credentials[0].Value != "hello world" {
		t.Errorf("set-credential value: got %q", desc.Credentials[0].Value)
	}
	if desc.Credentials[1].Value != "s3cr3t" {
		t.Errorf("set-credential value: got %q", desc.Credentials[1].Value)
	}
	if desc.Credentials[0].Path != "" {
		t.Error("set-credential should leave Path empty")
	}
}

func TestParseCredentialRejectsMissingColon(t *testing.T) {
	for _, line := range []string{
		"load-credential = noseparator\n",
		"set-credential = noseparator\n",
	} {
		input := "type = process\ncommand = /bin/true\n" + line
		if _, err := Parse(strings.NewReader(input), "svc", "test"); err == nil {
			t.Errorf("missing colon should error: %q", line)
		}
	}
}

func TestParseCredentialRejectsEmptyName(t *testing.T) {
	input := "type = process\ncommand = /bin/true\nload-credential = :/etc/x\n"
	if _, err := Parse(strings.NewReader(input), "svc", "test"); err == nil {
		t.Error("empty credential name should error")
	}
}

func TestSplitNameValue(t *testing.T) {
	cases := []struct {
		in         string
		name, val  string
		ok         bool
	}{
		{"k:v", "k", "v", true},
		{"k: v", "k", "v", true},
		{"k:v with spaces  ", "k", "v with spaces  ", true},
		{"k:", "k", "", true},
		{":v", "", "", false},
		{"no-colon", "", "", false},
		{"  spaces:v", "spaces", "v", true},
	}
	for _, c := range cases {
		n, v, ok := splitNameValue(c.in)
		if ok != c.ok {
			t.Errorf("%q: ok=%v want %v", c.in, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if n != c.name || v != c.val {
			t.Errorf("%q: got (%q,%q) want (%q,%q)", c.in, n, v, c.name, c.val)
		}
	}
}
