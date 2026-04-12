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
