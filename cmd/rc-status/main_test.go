package main

import (
	"bytes"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestTranslate_NoArgs(t *testing.T) {
	out, err := translate(nil)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if !reflect.DeepEqual(out, []string{"list"}) {
		t.Errorf("got %v, want [list]", out)
	}
}

func TestTranslate_Runlevel(t *testing.T) {
	out, err := translate([]string{"default"})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if !reflect.DeepEqual(out, []string{"graph", "runlevel-default"}) {
		t.Errorf("got %v, want [graph runlevel-default]", out)
	}
}

func TestTranslate_FlagsMappingToList(t *testing.T) {
	for _, flag := range []string{"-a", "--all", "-s", "--servicelist", "-u", "--unused"} {
		out, err := translate([]string{flag})
		if err != nil {
			t.Errorf("translate(%s): %v", flag, err)
			continue
		}
		if !reflect.DeepEqual(out, []string{"list"}) {
			t.Errorf("translate(%s) = %v, want [list]", flag, out)
		}
	}
}

func TestTranslate_ListRunlevelsSentinel(t *testing.T) {
	for _, f := range []string{"-l", "--list"} {
		if _, err := translate([]string{f}); err != errListRunlevels {
			t.Errorf("translate(%s) err=%v, want errListRunlevels", f, err)
		}
	}
}

func TestTranslate_CurrentRunlevelSentinel(t *testing.T) {
	for _, f := range []string{"-r", "--runlevel"} {
		if _, err := translate([]string{f}); err != errCurrentRunlevel {
			t.Errorf("translate(%s) err=%v, want errCurrentRunlevel", f, err)
		}
	}
}

func TestTranslate_Help(t *testing.T) {
	for _, f := range []string{"-h", "--help"} {
		if _, err := translate([]string{f}); err != errHelp {
			t.Errorf("translate(%s) err=%v, want errHelp", f, err)
		}
	}
}

func TestTranslate_UnknownFlag(t *testing.T) {
	if _, err := translate([]string{"--bogus"}); err == nil {
		t.Error("unknown flag should error")
	}
}

func TestRun_ListRunlevelsPrintsKnownNames(t *testing.T) {
	r, w, _ := os.Pipe()
	t.Cleanup(func() { r.Close() })

	rc := run([]string{"--list"}, w, os.Stderr)
	w.Close()
	if rc != 0 {
		t.Fatalf("rc = %d, want 0", rc)
	}

	var buf bytes.Buffer
	io.Copy(&buf, r)
	out := buf.String()
	for _, name := range openrcRunlevels {
		if !strings.Contains(out, name) {
			t.Errorf("output missing runlevel %q: %q", name, out)
		}
	}
}

func TestRun_CurrentRunlevelReportsDefault(t *testing.T) {
	r, w, _ := os.Pipe()
	t.Cleanup(func() { r.Close() })

	rc := run([]string{"-r"}, w, os.Stderr)
	w.Close()
	if rc != 0 {
		t.Fatalf("rc = %d, want 0", rc)
	}

	var buf bytes.Buffer
	io.Copy(&buf, r)
	if got := strings.TrimSpace(buf.String()); got != "default" {
		t.Errorf("current runlevel = %q, want default", got)
	}
}
