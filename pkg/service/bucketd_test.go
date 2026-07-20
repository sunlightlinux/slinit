package service

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseNotifyAccess covers the four values + a typo. Zero-value
// convention (unset → treated as Main by callers) is enforced at the
// enum decl level: NotifyAccessMain sits at iota=0.
func TestParseNotifyAccess(t *testing.T) {
	cases := []struct {
		in      string
		want    NotifyAccess
		wantErr bool
	}{
		{"", NotifyAccessMain, false},
		{"main", NotifyAccessMain, false},
		{"all", NotifyAccessAll, false},
		{"exec", NotifyAccessExec, false},
		{"none", NotifyAccessNone, false},
		{"bogus", 0, true},
	}
	for _, tc := range cases {
		got, err := ParseNotifyAccess(tc.in)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%q: err=%v want=%v", tc.in, err, tc.wantErr)
		}
		if err == nil && got != tc.want {
			t.Errorf("%q = %v want %v", tc.in, got, tc.want)
		}
	}
}

// TestApplyBucketDEnvFiltersDefault: no directive set = no changes
// to the env slice. The dinit-compatible default MUST be a no-op or
// every existing service loses env inheritance.
func TestApplyBucketDEnvFiltersDefault(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "svc")
	set.AddService(svc)
	in := []string{"HOME=/root", "PATH=/bin", "TERM=xterm"}
	got := svc.Record().applyBucketDEnvFilters(in)
	if len(got) != 3 {
		t.Fatalf("unset default should leave env untouched, got %v", got)
	}
}

// TestApplyBucketDEnvFiltersPassOnly: with pass-environment set,
// only listed vars survive — PLUS the SLINIT_* forced allow-list.
func TestApplyBucketDEnvFiltersPassOnly(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "svc")
	set.AddService(svc)
	svc.Record().SetPassEnvironment([]string{"HOME", "PATH"}, true)
	in := []string{"HOME=/root", "PATH=/bin", "TERM=xterm", "SLINIT_SERVICENAME=svc"}
	got := svc.Record().applyBucketDEnvFilters(in)
	if len(got) != 3 {
		t.Fatalf("want 3 (HOME, PATH, SLINIT_SERVICENAME), got %v", got)
	}
	for _, kv := range got {
		if kv == "TERM=xterm" {
			t.Errorf("TERM should have been filtered")
		}
	}
}

// TestApplyBucketDEnvFiltersUnset: unset-environment removes named
// vars, works alongside an unset pass-list.
func TestApplyBucketDEnvFiltersUnset(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "svc")
	set.AddService(svc)
	svc.Record().SetUnsetEnvironment([]string{"TERM", "PATH"})
	in := []string{"HOME=/root", "PATH=/bin", "TERM=xterm"}
	got := svc.Record().applyBucketDEnvFilters(in)
	if len(got) != 1 || got[0] != "HOME=/root" {
		t.Errorf("want [HOME=/root], got %v", got)
	}
}

// TestApplyBucketDEnvFiltersExecSearchPath: ExecSearchPath overrides
// an existing PATH= entry rather than appending a duplicate.
func TestApplyBucketDEnvFiltersExecSearchPath(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "svc")
	set.AddService(svc)
	svc.Record().SetExecSearchPath("/opt/bin")
	in := []string{"HOME=/root", "PATH=/bin"}
	got := svc.Record().applyBucketDEnvFilters(in)
	pathCount := 0
	for _, kv := range got {
		if len(kv) >= 5 && kv[:5] == "PATH=" {
			pathCount++
			if kv != "PATH=/opt/bin" {
				t.Errorf("PATH = %q, want /opt/bin", kv[5:])
			}
		}
	}
	if pathCount != 1 {
		t.Errorf("want exactly one PATH= entry, got %d in %v", pathCount, got)
	}
}

// TestGuessMainPIDFromCgroup: writes a synthetic cgroup.procs with a
// couple of pids and confirms the lowest non-self pid is picked.
// Uses a temp dir so no delegated cgroup is required.
func TestGuessMainPIDFromCgroup(t *testing.T) {
	dir := t.TempDir()
	body := []byte("99999\n88888\n77777\n")
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	pid, err := guessMainPIDFromCgroup(dir)
	if err != nil {
		t.Fatal(err)
	}
	if pid != 77777 {
		t.Errorf("want 77777 (lowest), got %d", pid)
	}
}

func TestGuessMainPIDFromCgroupEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := guessMainPIDFromCgroup(dir); err == nil {
		t.Errorf("expected error on empty cgroup.procs")
	}
}

func TestGuessMainPIDFromCgroupNoPath(t *testing.T) {
	if _, err := guessMainPIDFromCgroup(""); err == nil {
		t.Errorf("expected error on empty cgroup path")
	}
}
