package main

import "testing"

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
