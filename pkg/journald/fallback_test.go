package journald

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFallbackPrimarySucceeds(t *testing.T) {
	primary := t.TempDir()
	fallback := t.TempDir()
	fs, used, degraded := OpenFileSinkWithFallback(primary, fallback, FileSinkOptions{})
	if fs == nil {
		t.Fatalf("expected sink, got nil (degraded=%v)", degraded)
	}
	defer fs.Close()
	if used != primary {
		t.Fatalf("used=%q want %q", used, primary)
	}
	if degraded != nil {
		t.Fatalf("degraded should be nil on primary success: %v", degraded)
	}
}

func TestFallbackPrimaryUnwritable(t *testing.T) {
	// Read-only primary via chmod 0555 on an empty dir. mkdir will
	// succeed on parent-existing case; probe write should fail.
	root := t.TempDir()
	primary := filepath.Join(root, "readonly")
	if err := os.MkdirAll(primary, 0555); err != nil {
		t.Fatal(err)
	}
	// Non-root users can't write. Skip when running as root — the
	// permission bit would be bypassed and the test would falsely
	// pass. This is expected in CI (non-root) and typical dev
	// machines.
	if os.Getuid() == 0 {
		t.Skip("root bypasses 0555 permission; test only meaningful as non-root")
	}
	fallback := filepath.Join(root, "fallback")

	fs, used, degraded := OpenFileSinkWithFallback(primary, fallback, FileSinkOptions{})
	if fs == nil {
		t.Fatalf("expected sink from fallback, got nil (degraded=%v)", degraded)
	}
	defer fs.Close()
	if used != fallback {
		t.Fatalf("used=%q want fallback %q", used, fallback)
	}
	if degraded == nil {
		t.Fatal("expected degraded error when primary unwritable")
	}
}

func TestFallbackNoFallbackReturnsErr(t *testing.T) {
	// Path with /dev/null as parent — MkdirAll fails with ENOTDIR
	// regardless of UID, so this stays deterministic when the test
	// suite runs as root on CI (a plain non-existent path like
	// /no/such/parent/... would succeed because root can MkdirAll
	// anywhere).
	primary := "/dev/null/for/slinit-journald"

	fs, _, err := OpenFileSinkWithFallback(primary, "", FileSinkOptions{})
	if fs != nil {
		fs.Close()
		t.Fatal("expected nil sink when primary fails and no fallback set")
	}
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestProbeWritable(t *testing.T) {
	// Fresh temp dir must probe OK.
	if err := probeWritable(t.TempDir()); err != nil {
		t.Fatalf("probe on temp dir failed: %v", err)
	}
	// /dev/null as parent forces ENOTDIR on the MkdirAll attempt,
	// independent of UID — matters for CI running as root.
	if err := probeWritable("/dev/null/x/y/z"); err == nil {
		t.Fatal("expected probe error for uncreatable dir")
	}
}
