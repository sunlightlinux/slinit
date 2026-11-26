package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/machine"
)

// -M must refuse an entry whose PID now belongs to another process: the
// launcher was SIGKILLed, its entry stayed, and the PID was reused by a
// host process, whose /proc/PID/root is the host's root. Here the PID
// is this test's own, a host process.
func TestMachineReusedPIDRefused(t *testing.T) {
	dir := t.TempDir()
	prev := machine.Dir()
	machine.SetDir(dir)
	defer machine.SetDir(prev)

	self := strconv.Itoa(os.Getpid())
	// Written by the current version: a different start time.
	os.WriteFile(filepath.Join(dir, "web"), []byte(self+"\nSTARTTIME=1\n"), 0o644)
	// Written by an older version, before this process started.
	legacy := filepath.Join(dir, "old")
	os.WriteFile(legacy, []byte(self+"\n"), 0o644)
	past := time.Now().Add(-24 * time.Hour)
	os.Chtimes(legacy, past, past)

	for _, name := range []string{"web", "old"} {
		err := runQuery(options{machineTarget: name, quiet: true})
		if err == nil || !strings.Contains(err.Error(), "another process") {
			t.Errorf("-M %s on a reused PID: err = %v, want a refusal", name, err)
		}
	}
}

func TestLookupMachineAccepts(t *testing.T) {
	dir := t.TempDir()
	prev := machine.Dir()
	machine.SetDir(dir)
	defer machine.SetDir(prev)

	if err := machine.Register(machine.Machine{Name: "self", PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	if _, err := lookupMachine("self"); err != nil {
		t.Errorf("lookupMachine(registered live process) = %v", err)
	}
	if _, err := lookupMachine("nope"); err == nil || !strings.Contains(err.Error(), "no such machine") {
		t.Errorf("lookupMachine(unknown) = %v", err)
	}
}
