package service

import (
	"github.com/sunlightlinux/slinit/pkg/process"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The environment setting reaches the service; an env-file overrides
// it, as EnvironmentFile= overrides Environment=.
func TestInlineEnvironment(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	envFile := filepath.Join(dir, "env")
	os.WriteFile(envFile, []byte("B=from-file\n"), 0o644)
	s := NewProcessService(set, "e")
	s.SetCommand([]string{"/bin/sh", "-c", `echo "$A|$B|$C" > ` + out + `; exec sleep 60`})
	s.SetInlineEnv([]string{"A=one", "B=inline", "C=two words"})
	s.SetEnvFile(envFile)
	set.AddService(s)
	set.StartService(s)
	defer set.StopService(s)
	deadline := time.Now().Add(3 * time.Second)
	var b []byte
	for len(b) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		b, _ = os.ReadFile(out)
	}
	if got := strings.TrimSpace(string(b)); got != "one|from-file|two words" {
		t.Errorf("service saw %q, want one|from-file|two words", got)
	}
}

// A scripted service reads its env-file too, as a process service does
// (it read none, so a converted oneshot's EnvironmentFile= was empty).
func TestScriptedReadsEnvFile(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	envFile := filepath.Join(dir, "env")
	os.WriteFile(envFile, []byte("V=from-file\n"), 0o644)
	s := NewScriptedService(set, "j")
	s.SetStartCommand([]string{"/bin/sh", "-c", `echo "$V" > ` + out})
	s.SetEnvFile(envFile)
	set.AddService(s)
	set.StartService(s)
	defer set.StopService(s)
	waitState(t, s, StateStarted, 3*time.Second)
	if b, _ := os.ReadFile(out); strings.TrimSpace(string(b)) != "from-file" {
		t.Errorf("scripted saw %q", b)
	}
}

// A type-change reload carries over that a filter was configured, so
// the new record detaches one left on the cgroup.
func TestCarryFilterState(t *testing.T) {
	set, _ := newTestSet()
	old := NewProcessService(set, "f")
	old.Record().SetIPFilter(nil, []string{"any"})
	old.Record().SetSlice("x.slice")
	var p process.ExecParams
	old.Record().ApplyProcessAttrs(&p) // the start that attached it
	nu := NewScriptedService(set, "f")
	nu.Record().SetSlice("x.slice")
	nu.Record().CarryFilterState(old.Record())
	var q process.ExecParams
	nu.Record().ApplyProcessAttrs(&q)
	if !q.DetachIPFilter {
		t.Error("the new record does not detach the filter the old one attached")
	}
}
