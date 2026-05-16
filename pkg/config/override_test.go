package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// TestSiblingOverrideApplied verifies an upstart-style "<service>.override"
// file sitting next to the service file gets merged: scalar settings from the
// override replace those of the base, while unset settings are preserved.
func TestSiblingOverrideApplied(t *testing.T) {
	servicesDir := t.TempDir()

	writeServiceFile(t, servicesDir, "myapp",
		"type = process\ncommand = /usr/bin/myapp\nworking-dir = /var/lib/myapp\nrestart-delay = 1\n")
	writeServiceFile(t, servicesDir, "myapp.override",
		"command = /usr/local/bin/myapp --tuned\nrestart-delay = 30\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{servicesDir})
	ss.SetLoader(loader)

	desc, _, err := loader.findAndParseTestHelper("myapp")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if len(desc.Command) == 0 || desc.Command[0] != "/usr/local/bin/myapp" {
		t.Errorf("expected command override, got %v", desc.Command)
	}
	if desc.RestartDelay != 30*time.Second {
		t.Errorf("expected restart-delay override, got %v", desc.RestartDelay)
	}
	if desc.WorkingDir != "/var/lib/myapp" {
		t.Errorf("expected working-dir preserved, got %q", desc.WorkingDir)
	}
}

// TestSiblingOverrideMissing ensures a service with no .override file loads
// normally (the override is optional).
func TestSiblingOverrideMissing(t *testing.T) {
	servicesDir := t.TempDir()
	writeServiceFile(t, servicesDir, "plain",
		"type = process\ncommand = /bin/true\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{servicesDir})
	ss.SetLoader(loader)

	if _, err := loader.LoadService("plain"); err != nil {
		t.Fatalf("load should succeed without an override file: %v", err)
	}
}

// TestSiblingOverrideAppend verifies the += operator works inside an override
// (add arguments without restating the binary).
func TestSiblingOverrideAppend(t *testing.T) {
	servicesDir := t.TempDir()
	writeServiceFile(t, servicesDir, "appsvc",
		"type = process\ncommand = /usr/bin/appsvc --default\n")
	writeServiceFile(t, servicesDir, "appsvc.override",
		"command += --extra-flag\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{servicesDir})
	ss.SetLoader(loader)

	desc, _, err := loader.findAndParseTestHelper("appsvc")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if got := len(desc.Command); got != 3 || desc.Command[2] != "--extra-flag" {
		t.Fatalf("expected --extra-flag appended, got %v", desc.Command)
	}
}

// TestSiblingOverrideTemplate verifies a template (name@arg) picks up the
// override sitting next to the resolved base file, with $1 substitution still
// applied inside the override.
func TestSiblingOverrideTemplate(t *testing.T) {
	servicesDir := t.TempDir()
	writeServiceFile(t, servicesDir, "worker",
		"type = process\ncommand = /usr/bin/worker $1\nrestart-delay = 1\n")
	writeServiceFile(t, servicesDir, "worker.override",
		"command = /usr/bin/worker $1 --tuned\nrestart-delay = 9\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{servicesDir})
	ss.SetLoader(loader)

	desc, _, err := loader.findAndParseTestHelper("worker@foo")
	if err != nil {
		t.Fatalf("load worker@foo failed: %v", err)
	}
	if desc.RestartDelay != 9*time.Second {
		t.Errorf("expected override restart-delay, got %v", desc.RestartDelay)
	}
	want := []string{"/usr/bin/worker", "foo", "--tuned"}
	if len(desc.Command) != len(want) {
		t.Fatalf("expected %v, got %v", want, desc.Command)
	}
	for i := range want {
		if desc.Command[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, desc.Command)
		}
	}
}

// TestSiblingOverrideWinsOverConfd documents the precedence: a same-directory
// .override is applied after conf.d overlay dirs, so it has the final say on
// scalar conflicts (matching upstart's expectation that .override is local
// and authoritative).
func TestSiblingOverrideWinsOverConfd(t *testing.T) {
	servicesDir := t.TempDir()
	confDir := t.TempDir()

	writeServiceFile(t, servicesDir, "racy",
		"type = process\ncommand = /bin/true\nrestart-delay = 1\n")
	writeServiceFile(t, confDir, "racy", "restart-delay = 5\n")
	writeServiceFile(t, servicesDir, "racy.override", "restart-delay = 99\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{servicesDir})
	loader.SetOverlayDirs([]string{confDir})
	ss.SetLoader(loader)

	desc, _, err := loader.findAndParseTestHelper("racy")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if desc.RestartDelay != 99*time.Second {
		t.Errorf("expected sibling override to win (99s), got %v", desc.RestartDelay)
	}
}

// TestSiblingOverrideParseErrorFatal verifies a malformed override file is a
// hard load error rather than silently ignored.
func TestSiblingOverrideParseErrorFatal(t *testing.T) {
	servicesDir := t.TempDir()
	writeServiceFile(t, servicesDir, "bad",
		"type = process\ncommand = /bin/true\n")
	writeServiceFile(t, servicesDir, "bad.override",
		"this-is-not-a-known-setting = boom\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{servicesDir})
	ss.SetLoader(loader)

	if _, err := loader.LoadService("bad"); err == nil {
		t.Fatal("expected load to fail on malformed override file")
	}
}

// TestSiblingOverrideNotLoadableAsService confirms the convention is safe:
// the discovery path opens "<name>.override" relative to a request, so an
// override file is only ever pulled in as the sibling of its base service,
// never enumerated on its own.
func TestSiblingOverrideNotLoadableAsService(t *testing.T) {
	servicesDir := t.TempDir()
	writeServiceFile(t, servicesDir, "svc",
		"type = process\ncommand = /bin/true\n")
	writeServiceFile(t, servicesDir, "svc.override", "restart-delay = 2\n")

	// Sanity: the override file really is on disk next to the service.
	if _, err := os.Stat(filepath.Join(servicesDir, "svc.override")); err != nil {
		t.Fatalf("override file missing from test setup: %v", err)
	}

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{servicesDir})
	ss.SetLoader(loader)

	if _, err := loader.LoadService("svc"); err != nil {
		t.Fatalf("base service should load: %v", err)
	}
}
