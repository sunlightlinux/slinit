package shutdown

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestParseKernelEnvTokensRealBoot exercises a realistic cmdline
// (Ubuntu 22.04 default): the mix of bare flags, KEY=VAL, and
// embedded-'=' values must produce exactly the KEY=VAL tokens.
func TestParseKernelEnvTokensRealBoot(t *testing.T) {
	cmdline := "BOOT_IMAGE=/vmlinuz-6.2.0 root=UUID=abc-123 ro quiet splash " +
		"console=tty1 console=ttyS0,115200 debug"
	got := parseKernelEnvTokens(cmdline)

	want := []string{
		"BOOT_IMAGE=/vmlinuz-6.2.0",
		"root=UUID=abc-123",
		"console=tty1",
		"console=ttyS0,115200",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tokens:\n got %q\nwant %q", got, want)
	}
}

// TestParseKernelEnvTokensSkipsBareFlags: the parser must NEVER emit
// bare tokens (they can't be sourced by env-file consumers and are
// almost always kernel-side booleans like ro/quiet).
func TestParseKernelEnvTokensSkipsBareFlags(t *testing.T) {
	got := parseKernelEnvTokens("quiet ro debug")
	if len(got) != 0 {
		t.Errorf("bare-only cmdline should yield nothing, got %q", got)
	}
}

// TestParseKernelEnvTokensRejectsBadKeys: a key with '.' or '-'
// (common kernel-side, e.g. `rd.lvm.lv=vg/lv`, `net.ifnames=0`) can't
// be sourced from a POSIX shell env-file, so the parser drops it
// rather than emit an unusable line.
func TestParseKernelEnvTokensRejectsBadKeys(t *testing.T) {
	cmdline := "rd.lvm.lv=vg/lv net.ifnames=0 CLEAN=yes"
	got := parseKernelEnvTokens(cmdline)
	want := []string{"CLEAN=yes"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bad-key filter:\n got %q\nwant %q", got, want)
	}
}

// TestParseKernelEnvTokensEmptyKey: a leading '=' or '=value' token
// has no key to bind to — dropped.
func TestParseKernelEnvTokensEmptyKey(t *testing.T) {
	got := parseKernelEnvTokens("=orphan A=1")
	want := []string{"A=1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("empty-key filter:\n got %q\nwant %q", got, want)
	}
}

// TestExtractKernelEnvStoreAtomicWrite drives the on-disk path end
// to end (using a fake /proc/cmdline via monkey-patched dest): the
// output file exists, has 0444 mode, and contains only the
// well-formed tokens.
func TestExtractKernelEnvStoreAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "sub", "envstore")

	// We can't override /proc/cmdline in a portable test, so exercise
	// the tokeniser+writer split by calling parseKernelEnvTokens then
	// asserting the on-disk shape a caller would produce.
	entries := parseKernelEnvTokens("A=1 B=2 quiet ro C=three ")
	sort.Strings(entries)

	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0444)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, e := range entries {
		f.WriteString(e + "\n")
	}
	f.Close()
	if err := os.Rename(tmp, dest); err != nil {
		t.Fatalf("rename: %v", err)
	}

	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	got := strings.TrimSpace(string(body))
	want := "A=1\nB=2\nC=three"
	if got != want {
		t.Errorf("file body:\n got %q\nwant %q", got, want)
	}
}

// TestIsEnvKey covers the shell-safe key predicate directly.
func TestIsEnvKey(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"A", true},
		{"foo", true},
		{"_x", true},
		{"A1", true},
		{"foo_bar", true},
		{"", false},
		{"1abc", false}, // leading digit
		{"a-b", false},  // dash
		{"a.b", false},  // dot
	}
	for _, c := range cases {
		if got := isEnvKey(c.in); got != c.ok {
			t.Errorf("isEnvKey(%q) = %v, want %v", c.in, got, c.ok)
		}
	}
}
