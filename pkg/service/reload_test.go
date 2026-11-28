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

// A scripted job's reload has no bound with start-timeout 0 (a
// converted oneshot's), as systemd's; one a condition skipped is not
// running.
func TestReloadCommandScriptedBoundAndSkip(t *testing.T) {
	set, _ := newTestSet()
	s := NewScriptedService(set, "j")
	s.SetStartCommand([]string{"/bin/true"})
	s.SetReloadCommand([]string{"/bin/true"})
	s.SetStartTimeout(0)
	set.AddService(s)
	set.StartService(s)
	defer set.StopService(s)
	waitState(t, s, StateStarted, 3*time.Second)
	set.queueMu.Lock()
	spec, _ := s.reloadSpec()
	set.queueMu.Unlock()
	if !spec.running || spec.timeout != 0 {
		t.Errorf("start-timeout 0: running %v timeout %v, want no bound", spec.running, spec.timeout)
	}
	sk := NewScriptedService(set, "skipped")
	sk.SetStartCommand([]string{"/bin/true"})
	sk.SetReloadCommand([]string{"/bin/sh", "-c", "touch " + t.TempDir() + "/ran"})
	sk.Record().SetPredicates([]Predicate{{Kind: PredPathExists, Param: "/nonexistent/x"}})
	set.AddService(sk)
	set.StartService(sk)
	defer set.StopService(sk)
	waitState(t, sk, StateStarted, 3*time.Second)
	if has, err := set.RunReloadCommand(sk); has || err != nil {
		t.Errorf("skipped job: has %v err %v, want not running", has, err)
	}
}
