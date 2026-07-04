package main

import "testing"

func TestParseArgsStrictAndVerbose(t *testing.T) {
	opts, err := parseArgs([]string{"--strict", "--verbose", "a.conf"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.strict || !opts.verbose {
		t.Errorf("opts=%+v", opts)
	}
	if len(opts.files) != 1 || opts.files[0] != "a.conf" {
		t.Errorf("files=%v", opts.files)
	}
}

func TestParseArgsShortFlags(t *testing.T) {
	opts, err := parseArgs([]string{"-s", "-v"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.strict || !opts.verbose {
		t.Errorf("opts=%+v", opts)
	}
}

func TestParseArgsRootAttachedAndSpaced(t *testing.T) {
	if opts, err := parseArgs([]string{"--root=/x"}); err != nil || opts.root != "/x" {
		t.Errorf("attached: %+v %v", opts, err)
	}
	if opts, err := parseArgs([]string{"--root", "/y"}); err != nil || opts.root != "/y" {
		t.Errorf("spaced: %+v %v", opts, err)
	}
}

func TestParseArgsUnknownIsError(t *testing.T) {
	if _, err := parseArgs([]string{"--bogus"}); err == nil {
		t.Error("expected error")
	}
}
