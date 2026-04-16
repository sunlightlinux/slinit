package shutdown

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseRunMode(t *testing.T) {
	tests := []struct {
		in   string
		want RunMode
		err  bool
	}{
		{"", RunModeMount, false},
		{"mount", RunModeMount, false},
		{"fresh", RunModeMount, false},
		{"remount", RunModeRemount, false},
		{"keep", RunModeKeep, false},
		{"hands-off", RunModeKeep, false},
		{"none", RunModeKeep, false},
		{"bogus", RunModeMount, true},
		{"MOUNT", RunModeMount, true}, // case-sensitive on purpose
	}
	for _, tc := range tests {
		got, err := ParseRunMode(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("ParseRunMode(%q) err=nil, want err", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRunMode(%q) unexpected err: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ParseRunMode(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestSetRunMode_ClampsInvalid(t *testing.T) {
	orig := runMode
	t.Cleanup(func() { runMode = orig })

	SetRunMode(RunMode(99))
	if runMode != RunModeMount {
		t.Errorf("invalid mode should clamp to RunModeMount, got %v", runMode)
	}
}

func TestSetDevtmpfsPath(t *testing.T) {
	orig := devtmpfsPath
	t.Cleanup(func() { devtmpfsPath = orig })

	SetDevtmpfsPath("/custom/dev")
	if devtmpfsPath != "/custom/dev" {
		t.Errorf("devtmpfsPath = %q, want /custom/dev", devtmpfsPath)
	}

	// Empty disables the mount — exercised by mountEarlyFS.
	SetDevtmpfsPath("")
	if devtmpfsPath != "" {
		t.Errorf("empty path should be honoured literally, got %q", devtmpfsPath)
	}
}

func TestParentDir(t *testing.T) {
	tests := map[string]string{
		"/run/slinit/kcmdline": "/run/slinit",
		"/run/x":               "/run",
		"/file":                "/",
		"file":                 ".",
		"./a/b":                "./a",
	}
	for in, want := range tests {
		if got := parentDir(in); got != want {
			t.Errorf("parentDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSnapshotKernelCmdline(t *testing.T) {
	// Stage a fake /proc/cmdline via temp tree and write to a dest
	// inside the same temp dir. The function uses an absolute path
	// for /proc/cmdline so we can't redirect the source — instead
	// we rely on the real one existing on Linux test hosts.
	if _, err := os.Stat("/proc/cmdline"); err != nil {
		t.Skip("no /proc/cmdline available")
	}

	dir := t.TempDir()
	dest := filepath.Join(dir, "nested", "kcmdline")
	if err := snapshotKernelCmdline(dest); err != nil {
		t.Fatalf("snapshotKernelCmdline: %v", err)
	}

	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Error("snapshot is empty")
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0444 {
		t.Errorf("snapshot perm = %04o, want 0444", info.Mode().Perm())
	}
}

func TestSetKcmdlineDest(t *testing.T) {
	orig := kcmdlineDest
	t.Cleanup(func() { kcmdlineDest = orig })

	SetKcmdlineDest("")
	if kcmdlineDest != "" {
		t.Errorf("empty path should disable snapshot, got %q", kcmdlineDest)
	}
	SetKcmdlineDest("/run/foo")
	if kcmdlineDest != "/run/foo" {
		t.Errorf("kcmdlineDest = %q, want /run/foo", kcmdlineDest)
	}
}
