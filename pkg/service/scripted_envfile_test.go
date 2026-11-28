package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A scripted service reads its env-file, as a process service does.
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
