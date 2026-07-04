package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A representative /proc/mounts snapshot to drive filter tests
// against. Deliberately mixes pseudo, tmpfs, ext4, and nfs so every
// filter has entries to eliminate and keep.
const sampleProcMounts = `rootfs / rootfs rw 0 0
/dev/sda1 / ext4 rw,relatime 0 0
proc /proc proc rw 0 0
sysfs /sys sysfs rw,nosuid,nodev,noexec 0 0
tmpfs /run tmpfs rw,nosuid,mode=755,size=10% 0 0
/dev/sda2 /home ext4 rw,noatime 0 0
//nfs.example/share /mnt/nfs nfs rw,vers=4 0 0
tmpfs /tmp tmpfs rw,mode=1777 0 0
`

const sampleFstab = `# fstab
/dev/sda1 / ext4 defaults 0 1
/dev/sda2 /home ext4 defaults 0 2
//nfs.example/share /mnt/nfs nfs _netdev,ro 0 0
`

func writeFixtures(t *testing.T) (procPath, fstabPath string) {
	t.Helper()
	dir := t.TempDir()
	procPath = filepath.Join(dir, "mounts")
	fstabPath = filepath.Join(dir, "fstab")
	if err := os.WriteFile(procPath, []byte(sampleProcMounts), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fstabPath, []byte(sampleFstab), 0644); err != nil {
		t.Fatal(err)
	}
	return
}

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

func TestRunDefaultListsMountpointsReversed(t *testing.T) {
	proc, _ := writeFixtures(t)
	opts := options{
		field:      fieldMountPoint,
		procMounts: proc,
	}
	out, code := captureStdout(t, func() int { return run(opts) })
	if code != exitOK {
		t.Fatalf("code=%d", code)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// rootfs is skipped, so we expect 7 entries in reverse order:
	// /tmp first, / last.
	if len(lines) != 7 {
		t.Fatalf("lines=%d, want 7\n%s", len(lines), out)
	}
	if lines[0] != "/tmp" {
		t.Errorf("first line = %q, want /tmp (reverse-order default)", lines[0])
	}
	if lines[len(lines)-1] != "/" {
		t.Errorf("last line = %q, want /", lines[len(lines)-1])
	}
}

func TestRunSkipsRootfs(t *testing.T) {
	proc, _ := writeFixtures(t)
	out, _ := captureStdout(t, func() int {
		return run(options{procMounts: proc, field: fieldFstype})
	})
	if strings.Contains(out, "rootfs\n") {
		t.Errorf("rootfs should be skipped:\n%s", out)
	}
}

func TestRunFstypeRegex(t *testing.T) {
	proc, _ := writeFixtures(t)
	opts := options{
		procMounts: proc,
		field:      fieldMountPoint,
		fstypeRe:   regexp.MustCompile("^ext4$"),
	}
	out, _ := captureStdout(t, func() int { return run(opts) })
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// / and /home are ext4.
	if len(lines) != 2 {
		t.Fatalf("lines=%v", lines)
	}
	seen := map[string]bool{}
	for _, l := range lines {
		seen[l] = true
	}
	if !seen["/"] || !seen["/home"] {
		t.Errorf("expected / and /home, got %v", lines)
	}
}

func TestRunSkipFstypeRegex(t *testing.T) {
	proc, _ := writeFixtures(t)
	opts := options{
		procMounts:   proc,
		field:        fieldFstype,
		skipFstypeRe: regexp.MustCompile("tmpfs|proc|sysfs"),
	}
	out, _ := captureStdout(t, func() int { return run(opts) })
	if strings.Contains(out, "tmpfs") || strings.Contains(out, "proc") || strings.Contains(out, "sysfs") {
		t.Errorf("skip regex leaked:\n%s", out)
	}
}

func TestRunNodeRegex(t *testing.T) {
	proc, _ := writeFixtures(t)
	opts := options{
		procMounts: proc,
		field:      fieldMountPoint,
		nodeRe:     regexp.MustCompile("^/dev/sda"),
	}
	out, _ := captureStdout(t, func() int { return run(opts) })
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Errorf("lines=%v", lines)
	}
}

func TestRunOptionsRegex(t *testing.T) {
	proc, _ := writeFixtures(t)
	opts := options{
		procMounts: proc,
		field:      fieldMountPoint,
		optionsRe:  regexp.MustCompile("noatime"),
	}
	out, _ := captureStdout(t, func() int { return run(opts) })
	if strings.TrimSpace(out) != "/home" {
		t.Errorf("noatime → %q, want /home", out)
	}
}

func TestRunPointRegex(t *testing.T) {
	proc, _ := writeFixtures(t)
	opts := options{
		procMounts: proc,
		field:      fieldMountPoint,
		pointRe:    regexp.MustCompile("^/(t|r)"),
	}
	out, _ := captureStdout(t, func() int { return run(opts) })
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// /tmp, /run
	if len(lines) != 2 {
		t.Errorf("lines=%v", lines)
	}
}

func TestRunNetdevFilter(t *testing.T) {
	proc, fstab := writeFixtures(t)
	opts := options{
		procMounts: proc,
		etcFstab:   fstab,
		field:      fieldMountPoint,
		netdev:     netYes,
	}
	out, _ := captureStdout(t, func() int { return run(opts) })
	if strings.TrimSpace(out) != "/mnt/nfs" {
		t.Errorf("netdev → %q, want /mnt/nfs", out)
	}
}

func TestRunNonetdevFilter(t *testing.T) {
	proc, fstab := writeFixtures(t)
	opts := options{
		procMounts: proc,
		etcFstab:   fstab,
		field:      fieldMountPoint,
		netdev:     netNo,
	}
	out, _ := captureStdout(t, func() int { return run(opts) })
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// / and /home are in fstab without _netdev; other proc entries
	// are not in fstab so NetdevUnknown → excluded.
	seen := map[string]bool{}
	for _, l := range lines {
		seen[l] = true
	}
	if !seen["/"] || !seen["/home"] {
		t.Errorf("expected / and /home, got %v", lines)
	}
	if seen["/mnt/nfs"] {
		t.Errorf("nonetdev leaked _netdev entry")
	}
}

func TestRunPositionalFilter(t *testing.T) {
	proc, _ := writeFixtures(t)
	opts := options{
		procMounts:  proc,
		field:       fieldMountPoint,
		mountpoints: []string{"/tmp", "/home"},
	}
	out, code := captureStdout(t, func() int { return run(opts) })
	if code != exitOK {
		t.Errorf("code=%d", code)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// Positional list narrows to two entries, still reverse-ordered.
	if len(lines) != 2 || lines[0] != "/tmp" || lines[1] != "/home" {
		t.Errorf("lines=%v", lines)
	}
}

func TestRunNoMatchReturnsFailure(t *testing.T) {
	proc, _ := writeFixtures(t)
	opts := options{
		procMounts: proc,
		field:      fieldMountPoint,
		fstypeRe:   regexp.MustCompile("^xfs$"),
	}
	if code := run(opts); code != exitFailure {
		t.Errorf("code=%d, want %d", code, exitFailure)
	}
}

func TestRunOutputSelectors(t *testing.T) {
	proc, _ := writeFixtures(t)
	cases := []struct {
		field outputField
		want  string
	}{
		{fieldMountPoint, "/home"},
		{fieldOptions, "rw,noatime"},
		{fieldFstype, "ext4"},
		{fieldNode, "/dev/sda2"},
	}
	for _, tc := range cases {
		opts := options{
			procMounts:  proc,
			field:       tc.field,
			mountpoints: []string{"/home"},
		}
		out, _ := captureStdout(t, func() int { return run(opts) })
		if strings.TrimSpace(out) != tc.want {
			t.Errorf("field=%d → %q, want %q", tc.field, out, tc.want)
		}
	}
}

func TestParseArgsRegexFlags(t *testing.T) {
	opts, err := parseArgs([]string{
		"--fstype-regex", "^ext",
		"--skip-fstype-regex", "tmpfs",
		"--node-regex", "^/dev/sd",
		"--point-regex", "^/",
		"--options",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.fstypeRe == nil || opts.skipFstypeRe == nil || opts.nodeRe == nil || opts.pointRe == nil {
		t.Errorf("regexes not stored: %+v", opts)
	}
	if opts.field != fieldOptions {
		t.Errorf("field=%d, want %d", opts.field, fieldOptions)
	}
}

func TestParseArgsInvalidRegexFails(t *testing.T) {
	if _, err := parseArgs([]string{"--fstype-regex", "["}); err == nil {
		t.Errorf("expected error for bad regex")
	}
}

func TestParseArgsPositionalMustBeAbsolutePath(t *testing.T) {
	if _, err := parseArgs([]string{"not-a-path"}); err == nil {
		t.Errorf("expected error for non-absolute positional")
	}
}

func TestParseArgsNetdev(t *testing.T) {
	opts, err := parseArgs([]string{"--netdev"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.netdev != netYes {
		t.Errorf("netdev=%d", opts.netdev)
	}
	opts, err = parseArgs([]string{"--nonetdev"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.netdev != netNo {
		t.Errorf("nonetdev=%d", opts.netdev)
	}
}

func TestRunEinfoQuietSuppresses(t *testing.T) {
	t.Setenv("EINFO_QUIET", "yes")
	proc, _ := writeFixtures(t)
	out, code := captureStdout(t, func() int {
		return run(options{procMounts: proc, field: fieldMountPoint, mountpoints: []string{"/"}})
	})
	if code != exitOK {
		t.Errorf("code=%d", code)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("EINFO_QUIET should suppress, got %q", out)
	}
}
