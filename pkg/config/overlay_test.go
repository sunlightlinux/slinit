package config

import (
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// TestParseOverlayMergesIntoExistingDesc verifies that ParseOverlay re-uses
// the primary parser and correctly merges overrides into an already-populated
// ServiceDescription.
func TestParseOverlayMergesIntoExistingDesc(t *testing.T) {
	primary := `type = process
command = /usr/bin/myapp --default
working-dir = /var/lib/myapp
restart-delay = 1
`
	overlay := `command = /usr/local/bin/myapp --overridden --extra
restart-delay = 30
restart-delay-step = 5s
`

	desc, err := Parse(strings.NewReader(primary), "svc", "primary")
	if err != nil {
		t.Fatalf("primary parse failed: %v", err)
	}

	if err := ParseOverlay(strings.NewReader(overlay), "svc", "overlay", desc, nil); err != nil {
		t.Fatalf("overlay parse failed: %v", err)
	}

	// Override from overlay
	if len(desc.Command) < 2 || desc.Command[0] != "/usr/local/bin/myapp" {
		t.Errorf("expected command override, got %v", desc.Command)
	}
	if desc.Command[len(desc.Command)-1] != "--extra" {
		t.Errorf("expected full overridden args, got %v", desc.Command)
	}

	// Working-dir preserved from primary (not set in overlay)
	if desc.WorkingDir != "/var/lib/myapp" {
		t.Errorf("expected working-dir preserved, got %q", desc.WorkingDir)
	}

	// Restart-delay overridden (1s → 30s)
	if desc.RestartDelay != 30*time.Second {
		t.Errorf("expected restart-delay override, got %v", desc.RestartDelay)
	}

	// New field from overlay
	if desc.RestartDelayStep != 5*time.Second {
		t.Errorf("expected restart-delay-step from overlay, got %v", desc.RestartDelayStep)
	}
}

// TestParseOverlayAppendCommand verifies the += operator still works inside
// overlay files (e.g. adding arguments without re-specifying the binary).
func TestParseOverlayAppendCommand(t *testing.T) {
	primary := `type = process
command = /usr/bin/myapp --default
`
	overlay := `command += --extra-flag`

	desc, err := Parse(strings.NewReader(primary), "svc", "primary")
	if err != nil {
		t.Fatalf("primary parse failed: %v", err)
	}
	if err := ParseOverlay(strings.NewReader(overlay), "svc", "overlay", desc, nil); err != nil {
		t.Fatalf("overlay parse failed: %v", err)
	}
	if got := len(desc.Command); got != 3 {
		t.Fatalf("expected 3 command parts after += overlay, got %d: %v", got, desc.Command)
	}
	if desc.Command[2] != "--extra-flag" {
		t.Errorf("expected --extra-flag appended, got %v", desc.Command)
	}
}

// TestDirLoaderOverlayApplied verifies the full end-to-end loader path:
// a service file under servicesDir plus an overlay under confDir get merged
// when LoadService is called.
func TestDirLoaderOverlayApplied(t *testing.T) {
	servicesDir := t.TempDir()
	confDir := t.TempDir()

	writeServiceFile(t, servicesDir, "myapp",
		"type = process\ncommand = /usr/bin/myapp\nrestart-delay = 1\n")
	writeServiceFile(t, confDir, "myapp",
		"restart-delay = 42\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{servicesDir})
	loader.SetOverlayDirs([]string{confDir})
	ss.SetLoader(loader)

	svc, err := loader.LoadService("myapp")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if _, ok := svc.(*service.ProcessService); !ok {
		t.Fatalf("expected *ProcessService, got %T", svc)
	}

	// Re-inspect the parsed description to confirm overlay application.
	desc, _, err := loader.findAndParseTestHelper("myapp")
	if err != nil {
		t.Fatalf("re-parse failed: %v", err)
	}
	if desc.RestartDelay != 42*time.Second {
		t.Errorf("expected overlay-applied restart-delay=42s, got %v", desc.RestartDelay)
	}
	if len(desc.Command) == 0 || desc.Command[0] != "/usr/bin/myapp" {
		t.Errorf("primary command lost: %v", desc.Command)
	}
}

// TestDirLoaderOverlayMissing ensures a missing overlay file is not an error.
func TestDirLoaderOverlayMissing(t *testing.T) {
	servicesDir := t.TempDir()
	confDir := t.TempDir() // empty — no overlay file

	writeServiceFile(t, servicesDir, "plain",
		"type = process\ncommand = /bin/true\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{servicesDir})
	loader.SetOverlayDirs([]string{confDir})
	ss.SetLoader(loader)

	if _, err := loader.LoadService("plain"); err != nil {
		t.Fatalf("load should succeed with missing overlay: %v", err)
	}
}

// TestDirLoaderOverlayDisabled verifies that nil/empty overlayDirs
// disables overlay discovery entirely (even if files exist).
func TestDirLoaderOverlayDisabled(t *testing.T) {
	servicesDir := t.TempDir()
	confDir := t.TempDir()

	writeServiceFile(t, servicesDir, "disabled-test",
		"type = process\ncommand = /bin/true\nrestart-delay = 1\n")
	writeServiceFile(t, confDir, "disabled-test",
		"restart-delay = 999\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{servicesDir})
	loader.SetOverlayDirs(nil)
	ss.SetLoader(loader)

	desc, _, err := loader.findAndParseTestHelper("disabled-test")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if desc.RestartDelay != 1*time.Second {
		t.Errorf("overlay applied despite being disabled: got %v", desc.RestartDelay)
	}
}

// TestDirLoaderOverlayTemplateFallback verifies that templates
// (name@arg) also pick up overlays keyed by the base name.
func TestDirLoaderOverlayTemplateFallback(t *testing.T) {
	servicesDir := t.TempDir()
	confDir := t.TempDir()

	writeServiceFile(t, servicesDir, "worker",
		"type = process\ncommand = /usr/bin/worker $1\nrestart-delay = 1\n")
	// Overlay keyed by template base name (no @arg)
	writeServiceFile(t, confDir, "worker",
		"restart-delay = 77\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{servicesDir})
	loader.SetOverlayDirs([]string{confDir})
	ss.SetLoader(loader)

	desc, _, err := loader.findAndParseTestHelper("worker@foo")
	if err != nil {
		t.Fatalf("load worker@foo failed: %v", err)
	}
	if desc.RestartDelay != 77*time.Second {
		t.Errorf("expected template-base overlay to apply, got %v", desc.RestartDelay)
	}
}

// TestDirLoaderOverlayDefault verifies the default overlay directory is
// populated automatically (so /etc/slinit.conf.d is configured out-of-the-box).
func TestDirLoaderOverlayDefault(t *testing.T) {
	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{"/nonexistent"})
	if got := loader.OverlayDirs(); len(got) != 1 || got[0] != defaultOverlayDir {
		t.Errorf("expected default overlay dir %q, got %v", defaultOverlayDir, got)
	}
}

// findAndParseTestHelper exposes findAndParse to tests inside this package.
func (dl *DirLoader) findAndParseTestHelper(name string) (*ServiceDescription, string, error) {
	return dl.findAndParse(name)
}
