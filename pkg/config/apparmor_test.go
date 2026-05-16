package config

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// TestParseAppArmorLoadAndSwitch verifies both stanzas populate the
// description.
func TestParseAppArmorLoadAndSwitch(t *testing.T) {
	input := `type = process
command = /usr/bin/svc
apparmor-load = /etc/apparmor.d/usr.bin.svc
apparmor-switch = svc-profile
`
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if desc.AppArmorLoad != "/etc/apparmor.d/usr.bin.svc" {
		t.Errorf("apparmor-load = %q", desc.AppArmorLoad)
	}
	if desc.AppArmorSwitch != "svc-profile" {
		t.Errorf("apparmor-switch = %q", desc.AppArmorSwitch)
	}
}

// TestParseAppArmorLoadRelativeRejected verifies a non-absolute load path
// is a parse error (apparmor_parser is run with this path from the daemon).
func TestParseAppArmorLoadRelativeRejected(t *testing.T) {
	input := "type = process\ncommand = /bin/true\napparmor-load = profile\n"
	_, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err == nil || !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("expected absolute-path error, got %v", err)
	}
}

// TestParseAppArmorSwitchEmptyRejected verifies an empty switch value is
// rejected rather than silently disabling confinement.
func TestParseAppArmorSwitchEmptyRejected(t *testing.T) {
	input := "type = process\ncommand = /bin/true\napparmor-switch = \n"
	_, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err == nil {
		t.Fatal("expected error for empty apparmor-switch value")
	}
}

// TestAppArmorFlowsToRecord verifies the loader copies both values onto
// the ServiceRecord so the exec layer can apply them.
func TestAppArmorFlowsToRecord(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "confined",
		"type = process\ncommand = /usr/bin/confined\n"+
			"apparmor-load = /etc/apparmor.d/confined\napparmor-switch = confined-prof\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	svc, err := loader.LoadService("confined")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	load, prof := svc.Record().AppArmor()
	if load != "/etc/apparmor.d/confined" || prof != "confined-prof" {
		t.Errorf("AppArmor() = (%q, %q), want (/etc/apparmor.d/confined, confined-prof)", load, prof)
	}
}
