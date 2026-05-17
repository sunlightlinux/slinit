package config

import (
	"strings"
	"testing"
)

// TestParseServiceDirsAllFive verifies the five *-directory settings
// populate the matching name lists.
func TestParseServiceDirsAllFive(t *testing.T) {
	input := `type = process
command = /bin/true
runtime-directory = app app/sub
state-directory = app
cache-directory = app
logs-directory = app
configuration-directory = app
`
	desc, err := Parse(strings.NewReader(input), "svc", "tf")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(desc.RuntimeDirs) != 2 || desc.RuntimeDirs[0] != "app" || desc.RuntimeDirs[1] != "app/sub" {
		t.Errorf("runtime-directory = %v", desc.RuntimeDirs)
	}
	if len(desc.StateDirs) != 1 || len(desc.CacheDirs) != 1 ||
		len(desc.LogsDirs) != 1 || len(desc.ConfigDirs) != 1 {
		t.Errorf("expected one name each: state=%v cache=%v logs=%v config=%v",
			desc.StateDirs, desc.CacheDirs, desc.LogsDirs, desc.ConfigDirs)
	}
}

// TestParseServiceDirRejectsAbsolute verifies an absolute name is rejected
// (the loader prefixes a trusted base).
func TestParseServiceDirRejectsAbsolute(t *testing.T) {
	_, err := Parse(strings.NewReader(
		"type = process\ncommand = /bin/true\nstate-directory = /etc/passwd\n"), "svc", "tf")
	if err == nil || !strings.Contains(err.Error(), "must be relative") {
		t.Fatalf("expected relative-path error, got %v", err)
	}
}

// TestParseServiceDirRejectsDotDot verifies a '..' component is rejected.
func TestParseServiceDirRejectsDotDot(t *testing.T) {
	_, err := Parse(strings.NewReader(
		"type = process\ncommand = /bin/true\nruntime-directory = ../escape\n"), "svc", "tf")
	if err == nil || !strings.Contains(err.Error(), "'.'/'..' not allowed") {
		t.Fatalf("expected dotdot error, got %v", err)
	}
}

// TestParseServiceDirMode verifies octal mode parsing and rejection.
func TestParseServiceDirMode(t *testing.T) {
	desc, err := Parse(strings.NewReader(
		"type = process\ncommand = /bin/true\nstate-directory = a\nstate-directory-mode = 0700\n"), "svc", "tf")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if desc.StateDirMode == nil || *desc.StateDirMode != 0o700 {
		t.Errorf("expected state-directory-mode 0700, got %v", desc.StateDirMode)
	}
	if _, err := Parse(strings.NewReader(
		"type = process\ncommand = /bin/true\nstate-directory-mode = 999\n"), "svc", "tf"); err == nil {
		t.Fatal("expected error for non-octal mode 999")
	}
}

// TestParseRuntimeDirPreserve verifies no/yes/restart map to 0/1/2 and a
// bad value is rejected.
func TestParseRuntimeDirPreserve(t *testing.T) {
	cases := map[string]int{"no": 0, "yes": 1, "restart": 2}
	for v, want := range cases {
		desc, err := Parse(strings.NewReader(
			"type = process\ncommand = /bin/true\nruntime-directory-preserve = "+v+"\n"), "svc", "tf")
		if err != nil {
			t.Fatalf("%s: parse failed: %v", v, err)
		}
		if desc.RuntimeDirPreserve != want {
			t.Errorf("%s: got %d want %d", v, desc.RuntimeDirPreserve, want)
		}
	}
	if _, err := Parse(strings.NewReader(
		"type = process\ncommand = /bin/true\nruntime-directory-preserve = maybe\n"), "svc", "tf"); err == nil {
		t.Fatal("expected error for invalid preserve value")
	}
}

// TestParseServiceDirTemplateArg verifies $1 expansion inside a name.
func TestParseServiceDirTemplateArg(t *testing.T) {
	desc, err := ParseWithArg(strings.NewReader(
		"type = process\ncommand = /bin/true\nruntime-directory = svc-$1\n"),
		"svc@web", "tf", "web")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(desc.RuntimeDirs) != 1 || desc.RuntimeDirs[0] != "svc-web" {
		t.Errorf("expected $1 expanded to svc-web, got %v", desc.RuntimeDirs)
	}
}

// TestResolveServiceDirs verifies the loader maps names to absolute paths
// with the correct base, default mode 0755, and the volatile flag only on
// runtime-directory.
func TestResolveServiceDirs(t *testing.T) {
	desc, err := Parse(strings.NewReader(`type = process
command = /bin/true
runtime-directory = r
state-directory = s
cache-directory = c
logs-directory = l
configuration-directory = cfg
`), "svc", "tf")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	dirs := resolveServiceDirs(desc)
	want := map[string]bool{ // path -> volatile
		"/run/r":       true,
		"/var/lib/s":   false,
		"/var/cache/c": false,
		"/var/log/l":   false,
		"/etc/cfg":     false,
	}
	if len(dirs) != len(want) {
		t.Fatalf("expected %d dirs, got %d (%v)", len(want), len(dirs), dirs)
	}
	for _, d := range dirs {
		vol, ok := want[d.Path]
		if !ok {
			t.Errorf("unexpected path %q", d.Path)
			continue
		}
		if d.Volatile != vol {
			t.Errorf("%s volatile=%v want %v", d.Path, d.Volatile, vol)
		}
		if d.Mode != 0o755 {
			t.Errorf("%s mode=%o want 0755", d.Path, d.Mode)
		}
	}
}

// TestResolveServiceDirsModeOverride verifies a *-directory-mode override
// flows through to the resolved spec.
func TestResolveServiceDirsModeOverride(t *testing.T) {
	desc, err := Parse(strings.NewReader(
		"type = process\ncommand = /bin/true\nruntime-directory = r\nruntime-directory-mode = 0700\n"),
		"svc", "tf")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	dirs := resolveServiceDirs(desc)
	if len(dirs) != 1 || dirs[0].Mode != 0o700 {
		t.Fatalf("expected /run/r mode 0700, got %v", dirs)
	}
}
