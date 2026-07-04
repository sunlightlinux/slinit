package main

import (
	"os"
	"path/filepath"
	"testing"
)

// setStoreEnv points RC_SVCNAME + RC_SVCDIR at a scratch tree so the
// tests never touch /run/slinit. Restore is automatic via t.Setenv.
func setStoreEnv(t *testing.T, svcname string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("RC_SVCNAME", svcname)
	t.Setenv("RC_SVCDIR", dir)
	t.Setenv("SLINIT_SERVICENAME", "")
	return dir
}

func TestNewStoreReadsRcSvcname(t *testing.T) {
	setStoreEnv(t, "foo")
	s, err := newStore()
	if err != nil {
		t.Fatal(err)
	}
	if s.service != "foo" {
		t.Errorf("service=%q", s.service)
	}
	if filepath.Base(s.root) != "options" {
		t.Errorf("root basename = %q, want options", filepath.Base(s.root))
	}
}

func TestNewStoreFallsBackToSlinitEnv(t *testing.T) {
	t.Setenv("RC_SVCNAME", "")
	t.Setenv("RC_SVCDIR", t.TempDir())
	t.Setenv("SLINIT_SERVICENAME", "myservice")
	s, err := newStore()
	if err != nil {
		t.Fatal(err)
	}
	if s.service != "myservice" {
		t.Errorf("service=%q", s.service)
	}
}

func TestNewStoreErrorsWithoutServiceName(t *testing.T) {
	t.Setenv("RC_SVCNAME", "")
	t.Setenv("SLINIT_SERVICENAME", "")
	t.Setenv("RC_SVCDIR", t.TempDir())
	if _, err := newStore(); err == nil {
		t.Error("expected error when both env vars empty")
	}
}

func TestStoreSetThenGetRoundTrip(t *testing.T) {
	setStoreEnv(t, "svc")
	s, _ := newStore()
	if err := s.Set("port", "8080"); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get("port")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != "8080" {
		t.Errorf("got=%q ok=%v", got, ok)
	}
}

func TestStoreSetEmptyValueDeletes(t *testing.T) {
	setStoreEnv(t, "svc")
	s, _ := newStore()
	s.Set("key", "value")
	if _, ok, _ := s.Get("key"); !ok {
		t.Fatal("precondition: key should exist")
	}
	if err := s.Set("key", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get("key"); ok {
		t.Error("empty-value Set should delete")
	}
	// Deleting an already-missing key is a no-op, not an error.
	if err := s.Set("gone", ""); err != nil {
		t.Errorf("delete-missing: %v", err)
	}
}

func TestStoreGetMissReturnsOkFalse(t *testing.T) {
	setStoreEnv(t, "svc")
	s, _ := newStore()
	val, ok, err := s.Get("nope")
	if err != nil {
		t.Fatal(err)
	}
	if ok || val != "" {
		t.Errorf("got=%q ok=%v", val, ok)
	}
}

func TestStoreValidatesKey(t *testing.T) {
	setStoreEnv(t, "svc")
	s, _ := newStore()
	bad := []string{"", ".", "..", "a/b", "with\x00null"}
	for _, k := range bad {
		if _, _, err := s.Get(k); err == nil {
			t.Errorf("Get(%q): expected error", k)
		}
		if err := s.Set(k, "x"); err == nil {
			t.Errorf("Set(%q): expected error", k)
		}
	}
}

func TestStoreValuesArePersistedWithoutTrailingNewline(t *testing.T) {
	dir := setStoreEnv(t, "svc")
	s, _ := newStore()
	s.Set("multi", "line1\nline2")
	raw, err := os.ReadFile(filepath.Join(dir, "options", "svc", "multi"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "line1\nline2" {
		t.Errorf("raw=%q (must not append \\n)", raw)
	}
}

func TestStoreExportCapturesEnvOnlyWhenUnset(t *testing.T) {
	setStoreEnv(t, "svc")
	s, _ := newStore()
	t.Setenv("EXPORT_ME", "from-env")
	os.Unsetenv("EXPORT_MISSING")
	t.Setenv("EXPORT_FRESH", "captured-value")

	// Pre-set one key: Export must NOT overwrite it.
	s.Set("EXPORT_ME", "already-there")

	missing := s.Export([]string{"EXPORT_ME", "EXPORT_MISSING", "EXPORT_FRESH"})
	// EXPORT_ME already stored → skipped, prior value preserved.
	if got, _, _ := s.Get("EXPORT_ME"); got != "already-there" {
		t.Errorf("EXPORT_ME clobbered: %q", got)
	}
	// EXPORT_MISSING unset in env → reported as missing.
	if len(missing) != 1 || missing[0] != "EXPORT_MISSING" {
		t.Errorf("missing=%v, want [EXPORT_MISSING]", missing)
	}
	// EXPORT_FRESH not stored, present in env → captured.
	if got, ok, _ := s.Get("EXPORT_FRESH"); !ok || got != "captured-value" {
		t.Errorf("EXPORT_FRESH got=%q ok=%v", got, ok)
	}
}
