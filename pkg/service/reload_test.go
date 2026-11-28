package service

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A scripted job reloads by its command while started, without
// $MAINPID; stopped, it reports not running.
func TestReloadCommandScripted(t *testing.T) {
	set, _ := newTestSet()
	out := t.TempDir() + "/reloaded"
	s := NewScriptedService(set, "nft")
	s.SetStartCommand([]string{"/bin/true"})
	s.SetReloadCommand([]string{"/bin/sh", "-c", `echo "pid=${MAINPID:-none}" > ` + out})
	set.AddService(s)
	if has, err := set.RunReloadCommand(s); has || err != nil {
		t.Errorf("stopped: has %v err %v, want not running", has, err)
	}
	set.StartService(s)
	defer set.StopService(s)
	waitState(t, s, StateStarted, 3*time.Second)
	if has, err := set.RunReloadCommand(s); !has || err != nil {
		t.Fatalf("started: has %v err %v", has, err)
	}
	if b, _ := os.ReadFile(out); strings.TrimSpace(string(b)) != "pid=none" {
		t.Errorf("reload saw %q", b)
	}
}
