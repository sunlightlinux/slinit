package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout redirects stdout into a buffer for the duration of
// fn. Same pattern as the einfo / fstabinfo tests.
func captureStdout(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	rc := fn()
	w.Close()
	os.Stdout = old
	return <-done, rc
}

func TestDispatchGetHit(t *testing.T) {
	setStoreEnv(t, "svc")
	s, _ := newStore()
	s.Set("port", "8080")
	out, rc := captureStdout(t, func() int {
		return dispatch("service_get_value", []string{"port"})
	})
	if rc != exitOK {
		t.Errorf("rc=%d", rc)
	}
	// No trailing newline — matches OpenRC's behaviour.
	if out != "8080" {
		t.Errorf("out=%q", out)
	}
}

func TestDispatchGetMissReturnsFailure(t *testing.T) {
	setStoreEnv(t, "svc")
	out, rc := captureStdout(t, func() int {
		return dispatch("get_options", []string{"absent"})
	})
	if rc != exitFailure {
		t.Errorf("rc=%d, want %d", rc, exitFailure)
	}
	if out != "" {
		t.Errorf("unexpected output on miss: %q", out)
	}
}

func TestDispatchSetPersistsAcrossInvocations(t *testing.T) {
	setStoreEnv(t, "svc")
	if rc := dispatch("service_set_value", []string{"conf", "/etc/foo.conf"}); rc != exitOK {
		t.Fatalf("set rc=%d", rc)
	}
	s, _ := newStore()
	got, ok, _ := s.Get("conf")
	if !ok || got != "/etc/foo.conf" {
		t.Errorf("got=%q ok=%v", got, ok)
	}
}

func TestDispatchSaveOptionsAlias(t *testing.T) {
	setStoreEnv(t, "svc")
	// save_options is the legacy OpenRC alias for service_set_value.
	if rc := dispatch("save_options", []string{"k", "v"}); rc != exitOK {
		t.Fatalf("rc=%d", rc)
	}
	s, _ := newStore()
	got, ok, _ := s.Get("k")
	if !ok || got != "v" {
		t.Errorf("got=%q ok=%v", got, ok)
	}
}

func TestDispatchSetEmptyDeletes(t *testing.T) {
	setStoreEnv(t, "svc")
	dispatch("service_set_value", []string{"k", "v"})
	// Set with no VALUE arg → empty → delete.
	if rc := dispatch("service_set_value", []string{"k"}); rc != exitOK {
		t.Errorf("delete rc=%d", rc)
	}
	s, _ := newStore()
	if _, ok, _ := s.Get("k"); ok {
		t.Error("expected key to be gone")
	}
}

func TestDispatchExport(t *testing.T) {
	setStoreEnv(t, "svc")
	t.Setenv("MY_TUNABLE", "42")
	if rc := dispatch("service_export", []string{"MY_TUNABLE"}); rc != exitOK {
		t.Fatalf("rc=%d", rc)
	}
	s, _ := newStore()
	got, ok, _ := s.Get("MY_TUNABLE")
	if !ok || got != "42" {
		t.Errorf("MY_TUNABLE got=%q ok=%v", got, ok)
	}
}

func TestDispatchStripsSlinitPrefix(t *testing.T) {
	setStoreEnv(t, "svc")
	// A symlink installed as `slinit-service_get_value` should route
	// to service_get_value after the prefix strip in main().
	s, _ := newStore()
	s.Set("k", "v")
	out, rc := captureStdout(t, func() int {
		// dispatch already receives the stripped applet name, so
		// simulate what main() would pass.
		return dispatch("service_get_value", []string{"k"})
	})
	if rc != exitOK || out != "v" {
		t.Errorf("rc=%d out=%q", rc, out)
	}
}

func TestDispatchUnknownAppletFails(t *testing.T) {
	setStoreEnv(t, "svc")
	rc := dispatch("bogus_applet", nil)
	if rc == exitOK {
		t.Errorf("rc=%d, want non-zero", rc)
	}
}

func TestDispatchMissingKeyIsBadUsage(t *testing.T) {
	setStoreEnv(t, "svc")
	if rc := dispatch("service_get_value", nil); rc != exitBadUsage {
		t.Errorf("get with no arg: rc=%d, want %d", rc, exitBadUsage)
	}
	if rc := dispatch("service_set_value", nil); rc != exitBadUsage {
		t.Errorf("set with no arg: rc=%d, want %d", rc, exitBadUsage)
	}
	if rc := dispatch("service_export", nil); rc != exitBadUsage {
		t.Errorf("export with no arg: rc=%d, want %d", rc, exitBadUsage)
	}
}

func TestDispatchNoServiceEnvFailsFast(t *testing.T) {
	t.Setenv("RC_SVCNAME", "")
	t.Setenv("SLINIT_SERVICENAME", "")
	rc := dispatch("service_get_value", []string{"anything"})
	if rc != exitBadUsage {
		t.Errorf("rc=%d, want %d", rc, exitBadUsage)
	}
}

func TestPrintUsageMentionsAllApplets(t *testing.T) {
	out, rc := captureStdout(t, func() int {
		return dispatch("--help", nil)
	})
	if rc != exitOK {
		t.Errorf("rc=%d", rc)
	}
	for _, want := range []string{
		"service_get_value", "service_set_value", "service_export",
		"get_options", "save_options",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("usage missing %q", want)
		}
	}
}
