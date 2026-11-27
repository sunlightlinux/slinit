package main

import (
	"bytes"
	"flag"
	"io"
	"reflect"
	"strings"
	"testing"
)

// sysArgsFlags is the part of main's flag set the PID-1 scan has to get
// right: -s shares its variable with -m, as in main, and -t is the same
// repeatable list.
type sysArgsFlags struct {
	fs        *flag.FlagSet
	system    bool
	container bool
	services  stringSlice
	cgroup    string
	affinity  string
	envFile   string
	readyFD   int
	version   bool
}

func newSysArgsFlags() *sysArgsFlags {
	f := &sysArgsFlags{fs: flag.NewFlagSet("slinit", flag.ExitOnError)}
	f.fs.SetOutput(io.Discard)
	for _, n := range []string{"system", "s", "m", "system-mgr"} {
		f.fs.BoolVar(&f.system, n, false, "")
	}
	f.fs.BoolVar(&f.container, "o", false, "")
	f.fs.BoolVar(&f.container, "container", false, "")
	f.fs.Var(&f.services, "t", "")
	f.fs.Var(&f.services, "service", "")
	f.fs.StringVar(&f.cgroup, "b", "", "")
	f.fs.StringVar(&f.cgroup, "cgroup-path", "", "")
	f.fs.StringVar(&f.affinity, "a", "", "")
	f.fs.StringVar(&f.envFile, "e", "", "")
	f.fs.IntVar(&f.readyFD, "F", -1, "")
	f.fs.BoolVar(&f.version, "version", false, "")
	return f
}

// scan runs the PID-1 scan and returns the boot list main builds from
// it (-t values, then the accepted words) and what it reported.
func (f *sysArgsFlags) scan(args ...string) ([]string, string) {
	var w bytes.Buffer
	words := scanSysArgs(f.fs, args, &w)
	return append(append([]string{}, f.services...), words...), w.String()
}

func eq(a, b []string) bool { return len(a)+len(b) == 0 || reflect.DeepEqual(a, b) }

// TestScanSysArgs_DinitOrder: the PID-1 command line is read as
// dinit's process_commandline_arg loop reads it. Options take effect
// wherever they are; a naked word is a service only if it is "single",
// or once -m/-o has been seen — and only for the words after it; -s
// never makes stray words services.
func TestScanSysArgs_DinitOrder(t *testing.T) {
	for _, tc := range []struct {
		args      string
		want      []string
		system    bool
		container bool
	}{
		// An option after a naked word is honoured.
		{"splash -t foo", []string{"foo"}, false, false},
		{"splash auto -t foo nopti -t bar", []string{"foo", "bar"}, false, false},
		{"splash -service foo", []string{"foo"}, false, false},
		{"splash --service=foo", []string{"foo"}, false, false},
		// -s marks system mode; it never turns stray words into services.
		{"splash -s", nil, true, false},
		{"auto -s", nil, true, false},
		{"-s splash", nil, true, false},
		{"-s splash auto nopti", nil, true, false},
		{"--system splash", nil, true, false},
		{"splash -s single", []string{"single"}, true, false},
		// -m / -o: only the words after them are services.
		{"splash -m", nil, true, false},
		{"splash -m foo", []string{"foo"}, true, false},
		{"splash -m foo auto", []string{"foo", "auto"}, true, false},
		{"splash --system-mgr foo", []string{"foo"}, true, false},
		{"splash -o foo", []string{"foo"}, false, true},
		{"splash --container foo", []string{"foo"}, false, true},
		{"auto -s splash -m foo", []string{"foo"}, true, false},
		{"single -m foo", []string{"single", "foo"}, true, false},
		{"-m single", []string{"single"}, true, false},
		{"foo -t bar baz -m qux", []string{"bar", "qux"}, true, false},
		// -m=false is not -m.
		{"-m=false foo", nil, false, false},
		// "single" alone, and nothing at all.
		{"single", []string{"single"}, false, false},
		{"splash nopti auto", nil, false, false},
		{"", nil, false, false},
		// key=value words (dracut's after init=, or after the kernel's
		// own "--") are naked words like any other.
		{"rd.luks=0 root=/dev/sda splash -t foo", []string{"foo"}, false, false},
		{"-m rd.luks=0", []string{"rd.luks=0"}, true, false},
		// "--" is an unknown option, not the end of options.
		{"-- -t foo bar", []string{"foo"}, false, false},
		{"-m -- foo", []string{"foo"}, true, false},
		{"-z -- -z", nil, false, false},
		// An option's value is the next word, whatever it looks like.
		{"-t -s splash", []string{"-s"}, false, false},
		{"-t -m splash", []string{"-m"}, false, false},
		// dinit's invalid service names.
		{"-m .foo @bar ok", []string{"ok"}, true, false},
	} {
		f := newSysArgsFlags()
		got, _ := f.scan(strings.Fields(tc.args)...)
		if !eq(got, tc.want) || f.system != tc.system || f.container != tc.container {
			t.Errorf("%q: services=%q system=%v container=%v, want %q %v %v",
				tc.args, got, f.system, f.container, tc.want, tc.system, tc.container)
		}
	}
}

// TestScanSysArgs_ValuesAfterWords: value-taking options after naked
// words get their values, split or joined.
func TestScanSysArgs_ValuesAfterWords(t *testing.T) {
	f := newSysArgsFlags()
	got, _ := f.scan("-z", "junk", "splash", "-b", "/sys/fs/cgroup/x", "auto", "-F", "3",
		"single", "--a=0-3", "-e", "/etc/slinit.env", "--version")
	if f.cgroup != "/sys/fs/cgroup/x" || f.readyFD != 3 || f.affinity != "0-3" ||
		f.envFile != "/etc/slinit.env" || !f.version {
		t.Errorf("b=%q F=%d a=%q e=%q version=%v", f.cgroup, f.readyFD, f.affinity, f.envFile, f.version)
	}
	if !eq(got, []string{"single"}) {
		t.Errorf("services = %q", got)
	}
}

// TestScanSysArgs_LenientAsPID1: where dinit exits, PID 1 reports the
// item, ignores it and reads on.
func TestScanSysArgs_LenientAsPID1(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want []string
		note string
	}{
		{[]string{"-z", "-t", "foo"}, []string{"foo"}, "unrecognized option: -z"},
		{[]string{"-h", "--help", "-v", "-t", "foo"}, []string{"foo"}, "unrecognized option: --help"},
		{[]string{"-m", "foo", "-z", "bar"}, []string{"foo", "bar"}, "unrecognized option: -z"},
		{[]string{"-t", "foo", "-b"}, []string{"foo"}, "without its value: -b"},
		{[]string{"-t", "foo", "-t"}, []string{"foo"}, "without its value: -t"},
		{[]string{"-t", "", "-t", "foo"}, []string{"foo"}, "without its value: -t"},
		{[]string{"--service=", "-t", "foo"}, []string{"foo"}, "without its value: --service="},
		{[]string{"-F", "abc", "-t", "foo"}, []string{"foo"}, `ignoring -F "abc"`},
		{[]string{"-m", "", "foo"}, []string{"foo"}, "empty command-line argument"},
		{[]string{"---t", "x", "-t", "foo"}, []string{"foo"}, "unrecognized option: ---t"},
		{[]string{"-", "-t", "foo"}, []string{"foo"}, "unrecognized option: -"},
	} {
		f := newSysArgsFlags()
		got, w := f.scan(tc.args...)
		if !eq(got, tc.want) || !strings.Contains(w, tc.note) {
			t.Errorf("%q: services=%q report=%q, want %q and %q", tc.args, got, w, tc.want, tc.note)
		}
	}
	f := newSysArgsFlags()
	f.scan("-F", "abc")
	if f.readyFD != -1 {
		t.Errorf("-F abc set ready-fd to %d", f.readyFD)
	}
}

// TestScanSysArgs_LongLine: a -t at the end of a long line of kernel
// words is still read.
func TestScanSysArgs_LongLine(t *testing.T) {
	var args []string
	for i := 0; i < 2000; i++ {
		args = append(args, "word", "k=v", "-z")
	}
	args = append(args, "-t", "last")
	f := newSysArgsFlags()
	if got, _ := f.scan(args...); !eq(got, []string{"last"}) {
		t.Errorf("services = %q", got)
	}
}

// TestParseArgs_PID1DoesNotExit: none of this may exit init. An
// ExitOnError regression terminates the test binary, which fails the
// run just the same.
func TestParseArgs_PID1DoesNotExit(t *testing.T) {
	f := newSysArgsFlags()
	parseArgs(f.fs, []string{"-z", "-h", "-F", "abc", "-b", "--", "-t"}, true)
}

// TestParseArgs_NotPID1: outside PID 1 the line is the caller's and is
// parsed by flag as before; positional words come back unfiltered.
func TestParseArgs_NotPID1(t *testing.T) {
	f := newSysArgsFlags()
	got := parseArgs(f.fs, []string{"-t", "foo", "bar", "-t", "baz"}, false)
	if !eq(f.services, []string{"foo"}) || !eq(got, []string{"bar", "-t", "baz"}) {
		t.Errorf("services=%q words=%q", f.services, got)
	}
}
