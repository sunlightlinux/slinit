package main

import (
	"os"
	"path/filepath"
	"strings"
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

func TestParseLineUserUIDGID(t *testing.T) {
	for _, tc := range []struct{ in, uid, gid string }{
		{`u httpd 400:401`, "400", "401"},
		{`u httpd 400:web`, "400", "web"},
		{`u httpd 400`, "400", ""},
	} {
		e, err := parseLine(tc.in)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.in, err)
		}
		if e.idOrGid != tc.uid || e.gid != tc.gid {
			t.Errorf("parse %q: got uid=%q gid=%q, want %q %q", tc.in, e.idOrGid, e.gid, tc.uid, tc.gid)
		}
	}
	// g lines keep their ID untouched.
	if e, _ := parseLine(`g web 401`); e.idOrGid != "401" || e.gid != "" {
		t.Errorf("g line: got id=%q gid=%q", e.idOrGid, e.gid)
	}
}

func TestUserAddArgs(t *testing.T) {
	e, _ := parseLine(`u httpd 400:web "Web" /var/www /bin/sh`)
	got := strings.Join(userAddArgs(e), " ")
	want := "--system --uid 400 --gid web --comment Web --home-dir /var/www --shell /bin/sh httpd"
	if got != want {
		t.Errorf("useradd args:\n got %q\nwant %q", got, want)
	}
	e, _ = parseLine(`u nobody -`)
	got = strings.Join(userAddArgs(e), " ")
	want = "--system --no-create-home --shell /sbin/nologin nobody"
	if got != want {
		t.Errorf("useradd args:\n got %q\nwant %q", got, want)
	}
}

// TestCollectPrecedence: for the same file name /etc beats /run beats
// /usr/lib (systemd order), and files are merged and sorted by name.
func TestCollectPrecedence(t *testing.T) {
	root := t.TempDir()
	dirs := make([]string, len(defaultDirs))
	for i, d := range defaultDirs {
		dirs[i] = filepath.Join(root, d)
		if err := os.MkdirAll(dirs[i], 0755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(dir, name string) {
		if err := os.WriteFile(filepath.Join(root, dir, name), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("/usr/lib/sysusers.d", "all.conf")
	write("/run/sysusers.d", "all.conf")
	write("/etc/sysusers.d", "all.conf")
	write("/usr/lib/sysusers.d", "run-usr.conf")
	write("/run/sysusers.d", "run-usr.conf")

	got := collect(dirs)
	if p := filepath.Join(root, "/etc/sysusers.d/all.conf"); got["all.conf"] != p {
		t.Errorf("all.conf: got %q, want %q", got["all.conf"], p)
	}
	if p := filepath.Join(root, "/run/sysusers.d/run-usr.conf"); got["run-usr.conf"] != p {
		t.Errorf("run-usr.conf: got %q, want %q", got["run-usr.conf"], p)
	}
}
