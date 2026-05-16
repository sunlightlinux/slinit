package config

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// TestParseDebugYes verifies `debug = yes` sets the flag.
func TestParseDebugYes(t *testing.T) {
	desc, err := Parse(strings.NewReader(
		"type = process\ncommand = /bin/true\ndebug = yes\n"), "svc", "tf")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !desc.Debug {
		t.Error("expected Debug true")
	}
}

// TestParseDebugDefaultAndNo verifies the flag defaults off and `debug = no`
// keeps it off.
func TestParseDebugDefaultAndNo(t *testing.T) {
	d1, err := Parse(strings.NewReader(
		"type = process\ncommand = /bin/true\n"), "svc", "tf")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if d1.Debug {
		t.Error("expected Debug false by default")
	}
	d2, err := Parse(strings.NewReader(
		"type = process\ncommand = /bin/true\ndebug = no\n"), "svc", "tf")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if d2.Debug {
		t.Error("expected Debug false for `debug = no`")
	}
}

// TestParseDebugInvalid verifies a non-boolean value is rejected.
func TestParseDebugInvalid(t *testing.T) {
	_, err := Parse(strings.NewReader(
		"type = process\ncommand = /bin/true\ndebug = maybe\n"), "svc", "tf")
	if err == nil {
		t.Fatal("expected error for non-boolean debug value")
	}
}

// TestDebugFlowsToRecord verifies the loader copies the flag onto the
// ServiceRecord so the exec layer wraps with slinit-runner --debug.
func TestDebugFlowsToRecord(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "dbg",
		"type = process\ncommand = /usr/bin/dbg\ndebug = yes\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	svc, err := loader.LoadService("dbg")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if !svc.Record().Debug() {
		t.Error("expected Record().Debug() true")
	}
}
