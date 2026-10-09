package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/fstab"
)

const sampleFstab = `# a comment
/dev/sda1  /       ext4  defaults,noatime  0 1
/dev/sda2  none    swap  sw                0 0
UUID=abc   /home   ext4  rw,relatime       0 2
UUID=def   /boot   vfat  defaults          0 2
tmpfs      /tmp    tmpfs mode=1777         0 0
`

func writeSample(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fstab")
	if err := os.WriteFile(path, []byte(sampleFstab), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// captureStdout runs fn with os.Stdout redirected into a buffer and
// returns whatever fn printed.
func captureStdout(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	code := fn()
	w.Close()
	os.Stdout = old
	return <-done, code
}

func TestParseArgsPassnoOp(t *testing.T) {
	opts, err := parseArgs([]string{"--passno", "=2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.selectors) != 1 || opts.selectors[0].passnoOp != '=' || opts.selectors[0].passnoValue != 2 {
		t.Errorf("selectors=%+v", opts.selectors)
	}
}

func TestParseArgsPassnoPlain(t *testing.T) {
	opts, err := parseArgs([]string{"--passno", "/home"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.mode != outputPassno {
		t.Errorf("mode=%d", opts.mode)
	}
	if len(opts.selectors) != 1 || opts.selectors[0].file != "/home" || len(opts.files) != 0 {
		t.Errorf("selectors=%+v files=%v", opts.selectors, opts.files)
	}
}

func TestParseArgsFstype(t *testing.T) {
	opts, err := parseArgs([]string{"--fstype", "ext4,xfs"})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.selectors) != 1 || !slices.Equal(opts.selectors[0].fstypes, []string{"ext4", "xfs"}) {
		t.Errorf("selectors=%+v", opts.selectors)
	}
}

func TestRunDefaultListsAllMountpoints(t *testing.T) {
	entries := parseSample(t)
	out, code := captureStdout(t, func() int {
		return run(entries, options{mode: outputFile})
	})
	if code != exitOK {
		t.Errorf("code=%d", code)
	}
	// All five mount points listed.
	expected := []string{"/", "none", "/home", "/boot", "/tmp"}
	for _, want := range expected {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestRunBlockdeviceForMountpoint(t *testing.T) {
	entries := parseSample(t)
	out, code := captureStdout(t, func() int {
		return run(entries, options{mode: outputBlockDev, files: []string{"/home"}})
	})
	if code != exitOK {
		t.Errorf("code=%d", code)
	}
	if strings.TrimSpace(out) != "UUID=abc" {
		t.Errorf("out=%q", out)
	}
}

func TestRunOptionsForMountpoint(t *testing.T) {
	entries := parseSample(t)
	out, _ := captureStdout(t, func() int {
		return run(entries, options{mode: outputOptions, files: []string{"/"}})
	})
	if strings.TrimSpace(out) != "defaults,noatime" {
		t.Errorf("out=%q", out)
	}
}

func TestRunFstypeFilter(t *testing.T) {
	entries := parseSample(t)
	out, code := captureStdout(t, func() int {
		return run(entries, options{mode: outputFile, selectors: []selector{{fstypes: []string{"ext4"}}}})
	})
	if code != exitOK {
		t.Errorf("code=%d", code)
	}
	// / and /home are ext4; /tmp is tmpfs; swap and vfat excluded.
	if !strings.Contains(out, "/\n") || !strings.Contains(out, "/home\n") {
		t.Errorf("ext4 filter miss:\n%s", out)
	}
	if strings.Contains(out, "/tmp\n") || strings.Contains(out, "/boot\n") {
		t.Errorf("ext4 filter false positive:\n%s", out)
	}
}

func TestRunPassnoEquals(t *testing.T) {
	entries := parseSample(t)
	out, _ := captureStdout(t, func() int {
		return run(entries, options{mode: outputFile, selectors: []selector{{passnoOp: '=', passnoValue: 2}}})
	})
	// passno=2 → /home, /boot.
	got := strings.TrimSpace(out)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %v", len(lines), lines)
	}
	seen := map[string]bool{}
	for _, l := range lines {
		seen[l] = true
	}
	if !seen["/home"] || !seen["/boot"] {
		t.Errorf("expected /home + /boot, got %v", lines)
	}
}

func TestRunPassnoLessThan(t *testing.T) {
	entries := parseSample(t)
	// --passno <2 → entries with passno > 0 and < 2 → passno=1 only.
	out, _ := captureStdout(t, func() int {
		return run(entries, options{mode: outputFile, selectors: []selector{{passnoOp: '<', passnoValue: 2}}})
	})
	if strings.TrimSpace(out) != "/" {
		t.Errorf("want /, got %q", out)
	}
}

func TestRunMountArgs(t *testing.T) {
	entries := parseSample(t)
	out, _ := captureStdout(t, func() int {
		return run(entries, options{mode: outputMountArgs, files: []string{"/tmp"}})
	})
	// -o mode=1777 -t tmpfs tmpfs /tmp
	if !strings.Contains(out, "-o mode=1777") ||
		!strings.Contains(out, "-t tmpfs") ||
		!strings.Contains(out, " tmpfs /tmp") {
		t.Errorf("out=%q", out)
	}
}

func TestRunEmptyResultReturnsFailure(t *testing.T) {
	entries := parseSample(t)
	code := run(entries, options{mode: outputFile, files: []string{"/nope"}})
	if code != exitFailure {
		t.Errorf("code=%d, want %d", code, exitFailure)
	}
}

func TestRunEinfoQuietSuppressesPrint(t *testing.T) {
	t.Setenv("EINFO_QUIET", "yes")
	entries := parseSample(t)
	out, code := captureStdout(t, func() int {
		return run(entries, options{mode: outputFile, files: []string{"/"}})
	})
	if code != exitOK {
		t.Errorf("code=%d", code)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("EINFO_QUIET should suppress output, got %q", out)
	}
}

func TestParsePassNoArgVariants(t *testing.T) {
	op, val, plain, err := parsePassNoArg("=3")
	if err != nil || op != '=' || val != 3 || plain != "" {
		t.Errorf("=3: op=%c val=%d plain=%q err=%v", op, val, plain, err)
	}
	op, val, plain, err = parsePassNoArg("/data")
	if err != nil || op != 0 || val != 0 || plain != "/data" {
		t.Errorf("plain: op=%c val=%d plain=%q err=%v", op, val, plain, err)
	}
	if _, _, _, err := parsePassNoArg("=x"); err == nil {
		t.Errorf("expected error for =x")
	}
}

func parseSample(t *testing.T) []fstab.Entry {
	t.Helper()
	e, err := fstab.Parse(strings.NewReader(sampleFstab))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// TestEndToEndFromFile exercises main()'s file-load path via the
// documented `--file` seam.
func TestEndToEndFromFile(t *testing.T) {
	path := writeSample(t)
	opts, err := parseArgs([]string{"--file", path, "--blockdevice", "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fstab.ReadFile(opts.fstabPath)
	if err != nil {
		t.Fatal(err)
	}
	out, code := captureStdout(t, func() int { return run(entries, opts) })
	if code != exitOK {
		t.Errorf("code=%d", code)
	}
	if strings.TrimSpace(out) != "tmpfs" {
		t.Errorf("out=%q", out)
	}
}

// runArgs runs the tool against the sample fstab via the --file seam.
func runArgs(t *testing.T, args ...string) (string, int) {
	t.Helper()
	opts, err := parseArgs(append([]string{"--file", writeSample(t)}, args...))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fstab.ReadFile(opts.fstabPath)
	if err != nil {
		t.Fatal(err)
	}
	return captureStdout(t, func() int { return run(entries, opts) })
}

// Selection semantics of OpenRC's fstabinfo.c: every -t and -p OP N
// scans the whole fstab and appends to one list (union, duplicates
// kept, command-line order); positional names then filter that list,
// or become the list when it is empty.
func TestRunSelectionMatchesOpenRC(t *testing.T) {
	cases := []struct {
		name string
		args []string
		out  string
		code int
	}{
		{"fstype only", []string{"-t", "ext4"}, "/\n/home\n", exitOK},
		{"fstype list in token order", []string{"-t", "vfat,ext4"}, "/boot\n/\n/home\n", exitOK},
		{"fstype no match", []string{"-t", "nfs"}, "", exitFailure},
		{"passno only", []string{"-p", "=2"}, "/home\n/boot\n", exitOK},
		{"passno greater skips 0", []string{"-p", ">0"}, "/\n/home\n/boot\n", exitOK},
		{"fstype + passno is a union", []string{"-t", "ext4", "-p", "=2"}, "/\n/home\n/home\n/boot\n", exitOK},
		{"union in option order", []string{"-p", "=2", "-t", "vfat"}, "/home\n/boot\n/boot\n", exitOK},
		{"fstype no match + passno", []string{"-t", "nfs", "-p", "=2"}, "/home\n/boot\n", exitOK},
		{"repeated fstype", []string{"-t", "vfat", "-t", "tmpfs"}, "/boot\n/tmp\n", exitOK},
		{"positional only", []string{"/tmp", "/"}, "/tmp\n/\n", exitOK},
		{"positional missing sets 1", []string{"/home", "/nope"}, "/home\n", exitFailure},
		{"filter then positional keeps list order", []string{"-t", "ext4", "/home", "/", "/boot"}, "/\n/home\n", exitOK},
		{"filter then positional disjoint", []string{"-t", "ext4", "/boot"}, "", exitFailure},
		{"empty filter falls back to positional", []string{"-t", "nfs", "/boot"}, "/boot\n", exitOK},
		{"positional before options", []string{"/home", "-p", "=2"}, "/home\n", exitOK},
		{"mountargs over union", []string{"-m", "-t", "tmpfs", "-p", "=1"}, "-o mode=1777 -t tmpfs tmpfs /tmp\n-o defaults,noatime -t ext4 /dev/sda1 /\n", exitOK},
		// Plain -p is not a filter: with no positional arguments the
		// whole fstab is appended after the named mountpoint.
		{"plain passno", []string{"-p", "/home"}, "2\n1\n0\n2\n2\n0\n", exitOK},
		{"plain passno + positional", []string{"-p", "/home", "/home"}, "2\n", exitOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, code := runArgs(t, c.args...)
			if out != c.out {
				t.Errorf("out=%q, want %q", out, c.out)
			}
			if code != c.code {
				t.Errorf("code=%d, want %d", code, c.code)
			}
		})
	}
}
