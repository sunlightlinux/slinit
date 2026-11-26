package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/config"
	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// slinit-check did not enable the systemd and init.d fallbacks PID 1
// enables, so a depends-on naming a systemd unit was an ERROR here
// although the daemon loads it.
func TestCheckLoaderFindsSystemdUnits(t *testing.T) {
	native := t.TempDir()
	units := t.TempDir()
	if err := os.WriteFile(filepath.Join(native, "app"),
		[]byte("type = process\ncommand = /bin/true\ndepends-on: probe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(units, "probe.service"),
		[]byte("[Service]\nExecStart=/bin/true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	saved := config.DefaultSystemdDirs
	config.DefaultSystemdDirs = []string{units}
	defer func() { config.DefaultSystemdDirs = saved }()

	set := service.NewServiceSet(logging.New(logging.LevelError))
	if _, err := newCheckLoader(set, []string{native}).LoadService("app"); err != nil {
		t.Fatalf("app depends on a systemd unit PID 1 would load: %v", err)
	}
}
