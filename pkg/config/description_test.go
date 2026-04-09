package config

import (
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// TestDescriptionPropagatesToRecord verifies that a "description = ..." line
// in a service definition reaches the running ServiceRecord via applyToService.
// This is the runtime half of feature #9 — the parser was already filling
// ServiceDescription.Description, but until this wiring landed the value was
// dropped on the floor and slinitctl status could not display it.
func TestDescriptionPropagatesToRecord(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "greeter",
		"type = process\ncommand = /bin/true\ndescription = Friendly greeter service\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	svc, err := loader.LoadService("greeter")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	got := svc.Record().Description()
	want := "Friendly greeter service"
	if got != want {
		t.Errorf("Description() = %q, want %q", got, want)
	}
}

// TestDescriptionEmptyWhenUnset ensures services without a description leave
// the field empty — slinitctl status suppresses the line in that case.
func TestDescriptionEmptyWhenUnset(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "plain",
		"type = process\ncommand = /bin/true\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	svc, err := loader.LoadService("plain")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if got := svc.Record().Description(); got != "" {
		t.Errorf("Description() = %q, want empty", got)
	}
}
