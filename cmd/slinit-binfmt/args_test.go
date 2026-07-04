package main

import "testing"

func TestParseArgsUnregisterFlag(t *testing.T) {
	opts, err := parseArgs([]string{"--unregister"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.unregister {
		t.Error("unregister=false")
	}
}

func TestParseArgsPositionalFiles(t *testing.T) {
	opts, err := parseArgs([]string{"a.conf", "b.conf"})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.files) != 2 || opts.files[0] != "a.conf" || opts.files[1] != "b.conf" {
		t.Errorf("files=%v", opts.files)
	}
}

func TestParseArgsRoot(t *testing.T) {
	opts, err := parseArgs([]string{"--root=/tmp/x", "-v"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.root != "/tmp/x" {
		t.Errorf("root=%q", opts.root)
	}
	if !opts.verbose {
		t.Error("verbose=false")
	}
}

func TestParseArgsShortAttachedAndSpaced(t *testing.T) {
	// -u is the short for --unregister; --root=DIR / --root DIR both
	// should work.
	if opts, err := parseArgs([]string{"-u"}); err != nil || !opts.unregister {
		t.Errorf("-u: %+v %v", opts, err)
	}
	if opts, err := parseArgs([]string{"--root", "/x"}); err != nil || opts.root != "/x" {
		t.Errorf("--root spaced: %+v %v", opts, err)
	}
}

func TestParseArgsRejectsUnknown(t *testing.T) {
	if _, err := parseArgs([]string{"--bogus"}); err == nil {
		t.Error("expected error for --bogus")
	}
}
