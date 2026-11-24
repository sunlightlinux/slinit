package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTranslate_ServiceStart(t *testing.T) {
	out, err := translate([]string{"nginx", "start"})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if len(out) != 2 || out[0] != "start" || out[1] != "nginx" {
		t.Errorf("got %v, want [start nginx]", out)
	}
}

func TestTranslate_AllActionMappings(t *testing.T) {
	cases := map[string]string{
		"start":    "start",
		"stop":     "stop",
		"restart":  "restart",
		"status":   "status",
		"zap":      "release",
		"pause":    "pause",
		"continue": "continue",
	}
	for in, want := range cases {
		out, err := translate([]string{"svc", in})
		if err != nil {
			t.Fatalf("translate %s: %v", in, err)
		}
		if len(out) < 1 || out[0] != want {
			t.Errorf("%s → %v, want first=%q", in, out, want)
		}
	}
}

func TestTranslate_UnknownActionPassesThrough(t *testing.T) {
	// An action we haven't mapped shouldn't be blocked — it should
	// reach slinitctl which can then reject it with its own message.
	out, err := translate([]string{"svc", "reload"})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if len(out) != 2 || out[0] != "reload" || out[1] != "svc" {
		t.Errorf("got %v, want [reload svc]", out)
	}
}

func TestTranslate_ExistsFlag(t *testing.T) {
	out, err := translate([]string{"--exists", "nginx"})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if len(out) != 2 || out[0] != "is-started" {
		t.Errorf("got %v, want [is-started nginx]", out)
	}

	if _, err := translate([]string{"--exists"}); err == nil {
		t.Error("--exists without arg should error")
	}
}

func TestTranslate_ListFlag(t *testing.T) {
	out, err := translate([]string{"--list"})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if len(out) != 1 || out[0] != "list" {
		t.Errorf("got %v, want [list]", out)
	}
}

func TestTranslate_ResolveFlag(t *testing.T) {
	out, err := translate([]string{"--resolve", "sshd"})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if len(out) != 2 || out[0] != "query-name" {
		t.Errorf("got %v, want [query-name sshd]", out)
	}
}

func TestTranslate_Help(t *testing.T) {
	if _, err := translate([]string{"--help"}); err != errHelp {
		t.Errorf("--help err=%v, want errHelp", err)
	}
	if _, err := translate([]string{"-h"}); err != errHelp {
		t.Errorf("-h err=%v, want errHelp", err)
	}
}

func TestTranslate_Empty(t *testing.T) {
	if _, err := translate(nil); err == nil {
		t.Error("empty argv should error")
	}
}

func TestTranslate_MissingAction(t *testing.T) {
	if _, err := translate([]string{"svc"}); err == nil {
		t.Error("missing action should error")
	}
}

func TestTranslate_TrailingArgsPreserved(t *testing.T) {
	out, err := translate([]string{"svc", "start", "--flag"})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	// [start svc --flag]
	if len(out) != 3 || out[0] != "start" || out[1] != "svc" || out[2] != "--flag" {
		t.Errorf("got %v, want [start svc --flag]", out)
	}
}

// OpenRC's status exits with the state, not with success: scripts test
// `rc-service foo status` to see whether foo runs. A stopped service
// used to exit 0 because slinitctl status succeeds for any state.
func TestStatusExitCodeFollowsState(t *testing.T) {
	stub := filepath.Join(t.TempDir(), "slinitctl")
	script := "#!/bin/sh\ncase $1 in\nstatus) echo \"Service: $2\";;\nis-failed) echo \"$RCSVC_STATE\";;\nesac\n"
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	origBin, origExec := slinitctlBin, execFunc
	t.Cleanup(func() { slinitctlBin, execFunc = origBin, origExec })
	slinitctlBin = stub
	execFunc = func(string, []string, []string) error { return errors.New("exec'd") }

	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	for state, want := range map[string]int{
		"STARTED": 0, "STOPPED": 3, "STOPPING": 4, "STARTING": 8, "FAILED": 32,
	} {
		t.Setenv("RCSVC_STATE", state)
		if got := run([]string{"svc", "status"}, devnull, devnull); got != want {
			t.Errorf("status of a %s service exits %d, want %d", state, got, want)
		}
	}
}
