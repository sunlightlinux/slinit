package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func writeUnit(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const probeUnit = `[Unit]
Description=Probe
[Service]
ExecStart=/usr/bin/probe
User=probe
`

// The point of the feature: a name with no native description resolves
// to a .service unit and runs, with no file generated anywhere.
func TestDirLoaderResolvesSystemdUnit(t *testing.T) {
	root := t.TempDir()
	native := filepath.Join(root, "slinit.d")
	units := filepath.Join(root, "systemd")
	os.MkdirAll(native, 0o755)
	writeUnit(t, units, "probe.service", probeUnit)

	dl := NewDirLoader(service.NewServiceSet(&testConsumerLogger{}), []string{native})
	dl.SetSystemdDirs([]string{units})

	desc, path, err := dl.findAndParse("probe")
	if err != nil {
		t.Fatalf("probe should resolve to the unit: %v", err)
	}
	if filepath.Base(path) != "probe.service" {
		t.Errorf("resolved path = %q, want the unit file", path)
	}
	if desc.RunAs != "probe" {
		t.Errorf("run-as = %q, want probe", desc.RunAs)
	}
	if len(desc.Command) == 0 {
		t.Error("command is empty")
	}
}

// Operators migrating from systemctl type the extension out of habit.
func TestDirLoaderAcceptsExplicitServiceSuffix(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "systemd")
	writeUnit(t, units, "probe.service", probeUnit)

	dl := NewDirLoader(service.NewServiceSet(&testConsumerLogger{}), []string{filepath.Join(root, "slinit.d")})
	dl.SetSystemdDirs([]string{units})

	if _, _, err := dl.findAndParse("probe.service"); err != nil {
		t.Fatalf("probe.service should resolve: %v", err)
	}
}

// A native description must win. A distro shipping both must not have
// slinit silently prefer the file the admin did not write.
func TestNativeDescriptionBeatsSystemdUnit(t *testing.T) {
	root := t.TempDir()
	native := filepath.Join(root, "slinit.d")
	units := filepath.Join(root, "systemd")
	os.MkdirAll(native, 0o755)
	os.WriteFile(filepath.Join(native, "probe"), []byte("type = process\ncommand = /native/binary\n"), 0o644)
	writeUnit(t, units, "probe.service", probeUnit)

	dl := NewDirLoader(service.NewServiceSet(&testConsumerLogger{}), []string{native})
	dl.SetSystemdDirs([]string{units})

	desc, path, err := dl.findAndParse("probe")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) == "probe.service" {
		t.Fatal("the systemd unit shadowed the native description")
	}
	if desc.Command[0] != "/native/binary" {
		t.Errorf("command = %v, want the native one", desc.Command)
	}
}

// Earlier directories win, matching systemd's own precedence: an admin
// override in /etc must beat the packaged unit in /usr/lib.
func TestSystemdDirPrecedence(t *testing.T) {
	root := t.TempDir()
	etc := filepath.Join(root, "etc")
	usr := filepath.Join(root, "usr")
	writeUnit(t, etc, "probe.service", "[Service]\nExecStart=/etc/version\n")
	writeUnit(t, usr, "probe.service", "[Service]\nExecStart=/usr/version\n")

	dl := NewDirLoader(service.NewServiceSet(&testConsumerLogger{}), []string{filepath.Join(root, "slinit.d")})
	dl.SetSystemdDirs([]string{etc, usr})

	desc, _, err := dl.findAndParse("probe")
	if err != nil {
		t.Fatal(err)
	}
	if desc.Command[0] != "/etc/version" {
		t.Errorf("command = %v, want the /etc unit to win", desc.Command)
	}
}

// Only .service. A .timer or .socket has semantics that land on other
// slinit facilities, and loading one as a service would start something
// that never does what was asked.
func TestOnlyServiceUnitsAreLoaded(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "systemd")
	writeUnit(t, units, "probe.timer", "[Timer]\nOnCalendar=daily\n")
	writeUnit(t, units, "probe.socket", "[Socket]\nListenStream=9000\n")

	dl := NewDirLoader(service.NewServiceSet(&testConsumerLogger{}), []string{filepath.Join(root, "slinit.d")})
	dl.SetSystemdDirs([]string{units})

	for _, n := range []string{"probe", "probe.timer", "probe.socket"} {
		if _, _, err := dl.findAndParse(n); err == nil {
			t.Errorf("%q should not resolve: only .service units load", n)
		}
	}
}

// Disabled by default: without SetSystemdDirs nothing is searched.
func TestSystemdFallbackOffByDefault(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "systemd")
	writeUnit(t, units, "probe.service", probeUnit)

	dl := NewDirLoader(service.NewServiceSet(&testConsumerLogger{}), []string{filepath.Join(root, "slinit.d")})
	if _, _, err := dl.findAndParse("probe"); err == nil {
		t.Error("units resolved without SetSystemdDirs being called")
	}
}

// Conversion notes have no terminal to reach when a unit is loaded
// live, so they go through the hook. A dropped hardening directive the
// operator never hears about is the failure this prevents.
func TestSystemdWarningsReachTheHook(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "systemd")
	writeUnit(t, units, "probe.service", `[Service]
ExecStart=/usr/bin/probe
PrivateNetwork=yes
`)
	var got []string
	OnSystemdUnitWarning = func(svc, path, level, msg string) {
		got = append(got, level+": "+msg)
	}
	t.Cleanup(func() { OnSystemdUnitWarning = nil })

	dl := NewDirLoader(service.NewServiceSet(&testConsumerLogger{}), []string{filepath.Join(root, "slinit.d")})
	dl.SetSystemdDirs([]string{units})
	if _, _, err := dl.findAndParse("probe"); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Error("PrivateNetwork has no slinit equivalent and must be reported, not dropped silently")
	}
}
