package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pass-environment and unset-environment are documented to filter the
// inherited environment too. Only the variables slinit itself added
// were filtered: whatever the kernel or a login session put in slinit's
// own environment reached the service regardless.
func TestEnvFiltersApplyToTheInheritedEnvironment(t *testing.T) {
	t.Setenv("SLINIT_TEST_INHERITED_SECRET", "leaked")
	t.Setenv("SLINIT_TEST_INHERITED_KEEP", "kept")

	run := func(t *testing.T, setup func(*ProcessService)) string {
		t.Helper()
		set, _ := newTestSet()
		out := filepath.Join(t.TempDir(), "env")
		p := NewProcessService(set, "p")
		p.SetCommand([]string{"/bin/sh", "-c",
			"echo \"secret=$SLINIT_TEST_INHERITED_SECRET keep=$SLINIT_TEST_INHERITED_KEEP\" > " + out + "; exec sleep 30"})
		setup(p)
		set.AddService(p)
		set.StartService(p)
		defer set.StopService(p)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if b, err := os.ReadFile(out); err == nil && strings.HasSuffix(string(b), "\n") {
				return strings.TrimSpace(string(b))
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("the service never wrote its environment")
		return ""
	}

	if got := run(t, func(p *ProcessService) {
		p.Record().SetUnsetEnvironment([]string{"SLINIT_TEST_INHERITED_SECRET"})
	}); got != "secret= keep=kept" {
		t.Errorf("unset-environment: child saw %q, want the unset variable gone", got)
	}
	if got := run(t, func(p *ProcessService) {
		p.Record().SetPassEnvironment([]string{"SLINIT_TEST_INHERITED_KEEP"}, true)
	}); got != "secret= keep=kept" {
		t.Errorf("pass-environment: child saw %q, want only the listed variable", got)
	}
	if got := run(t, func(*ProcessService) {}); got != "secret=leaked keep=kept" {
		t.Errorf("no filter: child saw %q, want everything inherited as before", got)
	}
}
