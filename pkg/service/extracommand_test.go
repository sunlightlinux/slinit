package service

import (
	"sort"
	"testing"
)

func TestLookupExtraCommandAlways(t *testing.T) {
	sr := &ServiceRecord{}
	sr.SetExtraCommands(map[string][]string{
		"checkconfig": {"/bin/app", "--check"},
	})

	cmd, ok := sr.LookupExtraCommand("checkconfig")
	if !ok {
		t.Fatal("expected to find checkconfig")
	}
	if cmd[0] != "/bin/app" {
		t.Errorf("cmd = %v", cmd)
	}
}

func TestLookupExtraCommandStartedOnly(t *testing.T) {
	sr := &ServiceRecord{}
	sr.SetExtraStartedCommands(map[string][]string{
		"reload": {"/bin/app", "--reload"},
	})

	// Not started — should not find it
	sr.state = StateStopped
	_, ok := sr.LookupExtraCommand("reload")
	if ok {
		t.Error("reload should not be available when stopped")
	}

	// Started — should find it
	sr.state = StateStarted
	cmd, ok := sr.LookupExtraCommand("reload")
	if !ok {
		t.Fatal("expected to find reload when started")
	}
	if cmd[0] != "/bin/app" {
		t.Errorf("cmd = %v", cmd)
	}
}

func TestLookupExtraCommandNotFound(t *testing.T) {
	sr := &ServiceRecord{}
	sr.SetExtraCommands(map[string][]string{
		"checkconfig": {"/bin/app", "--check"},
	})

	_, ok := sr.LookupExtraCommand("nonexistent")
	if ok {
		t.Error("expected not to find nonexistent action")
	}
}

func TestListExtraActions(t *testing.T) {
	sr := &ServiceRecord{}
	sr.SetExtraCommands(map[string][]string{
		"checkconfig": {"/bin/app", "--check"},
		"validate":    {"/bin/app", "--validate"},
	})
	sr.SetExtraStartedCommands(map[string][]string{
		"reload": {"/bin/app", "--reload"},
	})

	actions := sr.ListExtraActions()
	sort.Strings(actions)
	if len(actions) != 3 {
		t.Fatalf("expected 3 actions, got %v", actions)
	}
	// reload should have * suffix (started-only)
	found := false
	for _, a := range actions {
		if a == "reload*" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected reload* in actions, got %v", actions)
	}
}
