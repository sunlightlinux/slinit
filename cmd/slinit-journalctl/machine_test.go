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
	// A different start time.
	os.WriteFile(filepath.Join(dir, "web"), []byte(self+"\nSTARTTIME=1\n"), 0o644)

	err := runQuery(options{machineTarget: "web", quiet: true})
	if err == nil || !strings.Contains(err.Error(), "another process") {
		t.Errorf("-M web on a reused PID: err = %v, want a refusal", err)
	}
}

// An entry written by an older slinit records no start time. Its live
// PID is used as that slinit used it, whenever the entry was written:
// judging it by the file's mtime against a boot time that moves with
// wall-clock steps refused running containers after an NTP sync.
func TestLookupMachineAcceptsLegacy(t *testing.T) {
	dir := t.TempDir()
	prev := machine.Dir()
	machine.SetDir(dir)
	defer machine.SetDir(prev)

	legacy := filepath.Join(dir, "old")
	os.WriteFile(legacy, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
	past := time.Now().Add(-24 * time.Hour)
	os.Chtimes(legacy, past, past)
	if _, err := lookupMachine("old"); err != nil {
		t.Errorf("lookupMachine(legacy entry, live PID) = %v", err)
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
