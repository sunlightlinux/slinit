package shutdown

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// fakeTTYs redirects ttyDir and ttyListFunc at a temp directory, creating
// regular files for each named TTY. It returns a map of name→path so tests
// can read back the captured output.
func fakeTTYs(t *testing.T, names ...string) map[string]string {
	t.Helper()
	dir := t.TempDir()

	origDir := ttyDir
	origList := ttyListFunc
	origHost := hostnameFunc
	t.Cleanup(func() {
		ttyDir = origDir
		ttyListFunc = origList
		hostnameFunc = origHost
	})

	ttyDir = dir
	ttyListFunc = func() []string { return names }
	hostnameFunc = func() (string, error) { return "testhost", nil }
	allowRegularTTYs(t)

	paths := make(map[string]string, len(names))
	for _, n := range names {
		p := filepath.Join(dir, n)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
		}
		f, err := os.Create(p)
		if err != nil {
			t.Fatalf("create %s: %v", p, err)
		}
		f.Close()
		paths[n] = p
	}
	return paths
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestWallWritesToAllTTYs(t *testing.T) {
	paths := fakeTTYs(t, "tty1", "pts/0")

	Wall("hello world", logging.New(logging.LevelDebug))

	for name, p := range paths {
		content := readFile(t, p)
		if !strings.Contains(content, "hello world") {
			t.Errorf("%s missing body: %q", name, content)
		}
		if !strings.Contains(content, "Broadcast message from slinit@testhost") {
			t.Errorf("%s missing banner: %q", name, content)
		}
	}
}

func TestWallNoUsersIsNoop(t *testing.T) {
	origList := ttyListFunc
	t.Cleanup(func() { ttyListFunc = origList })
	ttyListFunc = func() []string { return nil }

	// Must not panic with nil logger.
	Wall("nobody", nil)
}

func TestWallDisabled(t *testing.T) {
	paths := fakeTTYs(t, "tty1")

	SetWallEnabled(false)
	t.Cleanup(func() { SetWallEnabled(true) })

	Wall("should not appear", nil)

	if content := readFile(t, paths["tty1"]); content != "" {
		t.Errorf("tty1 should be empty, got %q", content)
	}
}

func TestWallShutdownNoticeImmediate(t *testing.T) {
	paths := fakeTTYs(t, "tty1")

	WallShutdownNotice(service.ShutdownReboot, 0, nil)

	content := readFile(t, paths["tty1"])
	if !strings.Contains(content, "going down for reboot NOW") {
		t.Errorf("missing immediate-reboot phrase: %q", content)
	}
}

func TestWallShutdownNoticeDelayed(t *testing.T) {
	paths := fakeTTYs(t, "tty1")

	WallShutdownNotice(service.ShutdownPoweroff, 5*time.Minute, nil)

	content := readFile(t, paths["tty1"])
	if !strings.Contains(content, "power-off") {
		t.Errorf("missing power-off label: %q", content)
	}
	if !strings.Contains(content, "5m") {
		t.Errorf("missing human duration: %q", content)
	}
}

func TestWallShutdownCancelled(t *testing.T) {
	paths := fakeTTYs(t, "tty1")

	WallShutdownCancelled(service.ShutdownHalt, nil)

	content := readFile(t, paths["tty1"])
	if !strings.Contains(content, "CANCELLED") {
		t.Errorf("missing CANCELLED: %q", content)
	}
	if !strings.Contains(content, "halt") {
		t.Errorf("missing halt label: %q", content)
	}
}

func TestWallPathEscapeDefense(t *testing.T) {
	// A malicious utmp entry like "../../../etc/passwd" must not escape ttyDir.
	origDir := ttyDir
	origList := ttyListFunc
	t.Cleanup(func() {
		ttyDir = origDir
		ttyListFunc = origList
	})

	dir := t.TempDir()
	ttyDir = dir
	ttyListFunc = func() []string { return []string{"../../../tmp/slinit-escape-test"} }

	// Should not create or write to /tmp/slinit-escape-test.
	Wall("escape", nil)

	if _, err := os.Stat("/tmp/slinit-escape-test"); err == nil {
		os.Remove("/tmp/slinit-escape-test")
		t.Fatal("wall escaped ttyDir — wrote to /tmp/slinit-escape-test")
	}
}

// allowRegularTTYs lets the tests stand regular files in for terminals.
func allowRegularTTYs(t *testing.T) {
	t.Helper()
	orig := isTerminal
	t.Cleanup(func() { isTerminal = orig })
	isTerminal = func(*os.File) bool { return true }
}

// ut_line comes from utmp, which anything in group utmp can write. A
// line that climbs out of /dev used to be kept — Clean leaves a leading
// ".." alone and TrimLeft only strips "/" — so PID 1 overwrote the
// start of the named file with the banner. The escape test above never
// caught it: its target did not exist and wall does not create files.
func TestWallDoesNotWriteOutsideTTYDir(t *testing.T) {
	root := t.TempDir()
	dev := filepath.Join(root, "dev")
	etc := filepath.Join(root, "etc")
	for _, d := range []string{dev, etc} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	passwd := filepath.Join(etc, "passwd")
	const orig = "root:x:0:0:root:/root:/bin/sh\n"
	if err := os.WriteFile(passwd, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeTTYs(t)
	ttyDir = dev
	ttyListFunc = func() []string { return []string{"../etc/passwd", "pts/../../etc/passwd"} }

	Wall("going down", nil)

	if got, _ := os.ReadFile(passwd); string(got) != orig {
		t.Fatalf("wall wrote outside %s: passwd now begins %q", dev, got[:min(len(got), 40)])
	}
}

// Inside /dev a line can still name a disk ("sda") or a symlink. Only a
// terminal may receive the broadcast: writing the banner to the start
// of a block device would destroy its partition table.
func TestWallWritesOnlyToTerminals(t *testing.T) {
	paths := fakeTTYs(t, "sda", "tty1")
	isTerminal = func(f *os.File) bool { return filepath.Base(f.Name()) == "tty1" }
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(ttyDir, "link")); err != nil {
		t.Fatal(err)
	}
	isTerminal = func(f *os.File) bool { return true }
	ttyListFunc = func() []string { return []string{"link"} }
	Wall("x", nil)
	if got := readFile(t, victim); got != "" {
		t.Errorf("wall followed a symlink in ttyDir: %q", got)
	}

	isTerminal = func(f *os.File) bool { return filepath.Base(f.Name()) == "tty1" }
	ttyListFunc = func() []string { return []string{"sda", "tty1"} }
	Wall("x", nil)
	if got := readFile(t, paths["sda"]); got != "" {
		t.Errorf("wall wrote to a non-terminal: %q", got)
	}
	if got := readFile(t, paths["tty1"]); got == "" {
		t.Error("wall skipped the terminal")
	}
}

func TestHumanDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{10 * time.Second, "10s"},
		{90 * time.Second, "1m"},
		{5 * time.Minute, "5m"},
		{2 * time.Hour, "2h"},
		{2*time.Hour + 30*time.Minute, "2h30m"},
	}
	for _, tc := range cases {
		if got := humanDuration(tc.d); got != tc.want {
			t.Errorf("humanDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestShutdownActionLabel(t *testing.T) {
	cases := map[service.ShutdownType]string{
		service.ShutdownReboot:     "reboot",
		service.ShutdownHalt:       "halt",
		service.ShutdownPoweroff:   "power-off",
		service.ShutdownSoftReboot: "soft-reboot",
		service.ShutdownKexec:      "kexec reboot",
	}
	for st, want := range cases {
		if got := shutdownActionLabel(st); got != want {
			t.Errorf("shutdownActionLabel(%v) = %q, want %q", st, got, want)
		}
	}
}

// Sanity check: ensure the formatted banner shape hasn't drifted.
func TestWallBannerFormat(t *testing.T) {
	paths := fakeTTYs(t, "tty1")

	Wall("body", nil)

	content := readFile(t, paths["tty1"])
	// Expect CRLF line endings and trailing blank line.
	if !bytes.Contains([]byte(content), []byte("\r\nbody\r\n")) {
		t.Errorf("body not CRLF-terminated: %q", content)
	}
}
