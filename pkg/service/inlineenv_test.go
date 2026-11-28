package service

import (
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
