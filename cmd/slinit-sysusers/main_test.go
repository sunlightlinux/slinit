package main

import (
	"testing"
)

func TestParseLineUser(t *testing.T) {
	e, err := parseLine(`u httpd 400 "Web server user" /var/www /sbin/nologin`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if e.kind != "u" || e.name != "httpd" || e.idOrGid != "400" {
		t.Errorf("got kind=%q name=%q id=%q, want u httpd 400", e.kind, e.name, e.idOrGid)
	}
	if e.gecos != "Web server user" {
		t.Errorf("gecos: got %q, want 'Web server user'", e.gecos)
	}
	if e.home != "/var/www" || e.shell != "/sbin/nologin" {
		t.Errorf("home/shell: got %q %q", e.home, e.shell)
	}
}

func TestParseLineGroup(t *testing.T) {
	e, err := parseLine(`g wheel 10`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if e.kind != "g" || e.name != "wheel" || e.idOrGid != "10" {
		t.Errorf("got kind=%q name=%q id=%q, want g wheel 10", e.kind, e.name, e.idOrGid)
	}
}

func TestParseLineMembership(t *testing.T) {
	e, err := parseLine(`m alice wheel`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if e.kind != "m" || e.name != "alice" || e.arg != "wheel" {
		t.Errorf("got kind=%q name=%q arg=%q, want m alice wheel", e.kind, e.name, e.arg)
	}
}

func TestParseLineDefaults(t *testing.T) {
	e, err := parseLine(`u nobody - - - -`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if e.idOrGid != "" || e.gecos != "" || e.home != "" || e.shell != "" {
		t.Errorf("all-dash line must leave optional fields empty: %+v", e)
	}
}

func TestParseLineRejectsShort(t *testing.T) {
	if _, err := parseLine("u"); err == nil {
		t.Error("single-field line must be rejected")
	}
}
