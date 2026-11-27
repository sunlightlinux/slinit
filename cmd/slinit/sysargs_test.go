package main

import (
	"flag"
	"io"
	"reflect"
	"testing"
)

func sysArgsFlagSet() (*flag.FlagSet, *string, *bool, *string) {
	fs := flag.NewFlagSet("slinit", flag.ExitOnError)
	fs.SetOutput(io.Discard)
	cg := fs.String("b", "", "")
	fs.String("cgroup-path", "", "")
	ver := fs.Bool("version", false, "")
	aff := fs.String("a", "", "")
	return fs, cg, ver, aff
}

// TestParseArgs_PID1IgnoresKernelWords: as PID 1 the kernel passes
// unknown boot parameters to init. An ExitOnError parse of any of
// them exited init — a kernel panic. They must be dropped instead.
// An ExitOnError regression terminates the test binary, which fails
// the run just the same.
func TestParseArgs_PID1IgnoresKernelWords(t *testing.T) {
	for _, tc := range []struct {
		args []string
		rest []string
	}{
		{[]string{"-z", "--version"}, []string{}},
		{[]string{"-h", "-v", "--version"}, []string{}},
		{[]string{"--version", "-b"}, []string{}},
		{[]string{"--version", "-a"}, []string{}},
		{[]string{"--nosuch=1", "--version", "nopti", "-z"}, []string{"nopti"}},
		{[]string{"-z", "--version", "--", "-z"}, []string{"-z"}},
	} {
		fs, _, ver, _ := sysArgsFlagSet()
		parseArgs(fs, tc.args, true)
		if !*ver {
			t.Errorf("%q: --version not parsed", tc.args)
		}
		if got := fs.Args(); len(got)+len(tc.rest) != 0 && !reflect.DeepEqual(got, tc.rest) {
			t.Errorf("%q: positional = %q, want %q", tc.args, got, tc.rest)
		}
	}
}

// TestParseArgs_PID1KeepsKnownOptions: filtering must not disturb a
// well-formed slinit option, with its value split or joined.
func TestParseArgs_PID1KeepsKnownOptions(t *testing.T) {
	fs, cg, _, aff := sysArgsFlagSet()
	parseArgs(fs, []string{"-z", "-b", "/sys/fs/cgroup/x", "--a=0-3", "single"}, true)
	if *cg != "/sys/fs/cgroup/x" || *aff != "0-3" {
		t.Errorf("b=%q a=%q", *cg, *aff)
	}
	if got := fs.Args(); !reflect.DeepEqual(got, []string{"single"}) {
		t.Errorf("positional = %q", got)
	}
}

// TestParseArgs_PID1OptionsAfterAWord: the kernel passes init words
// it does not know (`splash`, LILO's `auto`, the value of sysvinit's
// `-z`). Flag parsing stops at the first of them, so every option after
// it, `-t` included, was silently dropped. dinit scans the whole line;
// so must slinit.
func TestParseArgs_PID1OptionsAfterAWord(t *testing.T) {
	fs, cg, ver, aff := sysArgsFlagSet()
	parseArgs(fs, []string{"-z", "junk", "splash", "-b", "/sys/fs/cgroup/x", "single", "--version", "--a=0-3"}, true)
	if *cg != "/sys/fs/cgroup/x" || !*ver || *aff != "0-3" {
		t.Errorf("after a naked word: b=%q version=%v a=%q", *cg, *ver, *aff)
	}
	if got := fs.Args(); !reflect.DeepEqual(got, []string{"junk", "splash", "single"}) {
		t.Errorf("positional = %q", got)
	}
}

// TestParseArgs_PID1BadValueDoesNotExit: a value that fails to
// parse stops parsing but must not exit init.
func TestParseArgs_PID1BadValueDoesNotExit(t *testing.T) {
	fs := flag.NewFlagSet("slinit", flag.ExitOnError)
	fs.SetOutput(io.Discard)
	fs.Int("F", -1, "")
	parseArgs(fs, []string{"-F", "abc"}, true)
}
