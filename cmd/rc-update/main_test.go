package main

import (
	"reflect"
	"testing"
)

func TestTranslate_Add_Default(t *testing.T) {
	out, err := translate([]string{"add", "nginx"})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	want := []string{"--from", "runlevel-default", "enable", "nginx"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("got %v, want %v", out, want)
	}
}

func TestTranslate_Add_ExplicitRunlevel(t *testing.T) {
	out, err := translate([]string{"add", "nginx", "boot"})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	want := []string{"--from", "runlevel-boot", "enable", "nginx"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("got %v, want %v", out, want)
	}
}

func TestTranslate_Del_MapsToDisable(t *testing.T) {
	out, err := translate([]string{"del", "nginx", "default"})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	want := []string{"--from", "runlevel-default", "disable", "nginx"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("got %v, want %v", out, want)
	}

	// `delete` is an accepted alias of `del`.
	out, err = translate([]string{"delete", "nginx", "default"})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("delete → %v, want %v", out, want)
	}
}

func TestTranslate_Show(t *testing.T) {
	out, err := translate([]string{"show"})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	want := []string{"graph", "runlevel-default"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("show → %v, want %v", out, want)
	}

	out, err = translate([]string{"show", "boot"})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	want = []string{"graph", "runlevel-boot"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("show boot → %v, want %v", out, want)
	}
}

func TestTranslate_UpdateIsNoop(t *testing.T) {
	if _, err := translate([]string{"update"}); err != errNoop {
		t.Errorf("update err=%v, want errNoop", err)
	}
	if _, err := translate([]string{"-u"}); err != errNoop {
		t.Errorf("-u err=%v, want errNoop", err)
	}
}

func TestTranslate_Help(t *testing.T) {
	for _, a := range []string{"", "--help", "-h"} {
		var argv []string
		if a != "" {
			argv = []string{a}
		}
		if _, err := translate(argv); err != errHelp {
			t.Errorf("translate(%q) err=%v, want errHelp", a, err)
		}
	}
}

func TestTranslate_Add_MissingService(t *testing.T) {
	if _, err := translate([]string{"add"}); err == nil {
		t.Error("add with no args should error")
	}
}

func TestTranslate_Add_TooManyArgs(t *testing.T) {
	if _, err := translate([]string{"add", "a", "b", "c"}); err == nil {
		t.Error("add with 3 args should error")
	}
}

func TestTranslate_Add_InvalidNames(t *testing.T) {
	if _, err := translate([]string{"add", "bad/name", "default"}); err == nil {
		t.Error("service with / should be rejected")
	}
	if _, err := translate([]string{"add", "nginx", "bad/level"}); err == nil {
		t.Error("runlevel with / should be rejected")
	}
}

func TestTranslate_UnknownVerbPassesThrough(t *testing.T) {
	// Unknown verb → forward verbatim so slinitctl can emit its own
	// error. This future-proofs us against OpenRC verbs we haven't
	// mapped yet.
	out, err := translate([]string{"future-verb", "arg"})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	want := []string{"future-verb", "arg"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("got %v, want %v", out, want)
	}
}

func TestRunlevelService(t *testing.T) {
	if got := runlevelService("default"); got != "runlevel-default" {
		t.Errorf("got %q, want runlevel-default", got)
	}
	if got := runlevelService("nonetwork"); got != "runlevel-nonetwork" {
		t.Errorf("got %q, want runlevel-nonetwork", got)
	}
}
