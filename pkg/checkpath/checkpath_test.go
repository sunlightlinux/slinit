package checkpath

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// TestApplyCreatesDirectory verifies the -d code path: a missing directory
// is created with the requested mode.
func TestApplyCreatesDirectory(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "new-dir")

	res, err := Apply(Spec{
		Path:  target,
		Type:  TypeDir,
		Mode:  0o750,
		Owner: Owner{UID: -1, GID: -1},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.Created {
		t.Error("expected Created=true")
	}
	st, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !st.IsDir() {
		t.Errorf("expected dir, got mode %v", st.Mode())
	}
	if perm := st.Mode().Perm(); perm != 0o750 {
		t.Errorf("mode = %o, want 0750", perm)
	}
}

// TestApplyCreatesFile verifies the -f code path for regular files.
func TestApplyCreatesFile(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "f.txt")

	res, err := Apply(Spec{
		Path:  target,
		Type:  TypeFile,
		Mode:  0o600,
		Owner: Owner{UID: -1, GID: -1},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.Created {
		t.Error("expected Created=true")
	}
	st, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 {
		t.Errorf("unexpected mode %v", st.Mode())
	}
}

// TestApplyCreatesFifo verifies named-pipe creation.
func TestApplyCreatesFifo(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "p")

	if _, err := Apply(Spec{
		Path:  target,
		Type:  TypeFifo,
		Mode:  0o600,
		Owner: Owner{UID: -1, GID: -1},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	st, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("not a FIFO: %v", st.Mode())
	}
}

// TestApplyCorrectsMode verifies that an existing path with the wrong mode
// gets chmodded on Apply, and that the result flag reflects the change.
func TestApplyCorrectsMode(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "d")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	res, err := Apply(Spec{
		Path:  target,
		Type:  TypeDir,
		Mode:  0o755,
		Owner: Owner{UID: -1, GID: -1},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Created {
		t.Error("unexpected Created=true")
	}
	if !res.ChMod {
		t.Error("expected ChMod=true")
	}
	st, _ := os.Lstat(target)
	if st.Mode().Perm() != 0o755 {
		t.Errorf("mode = %o, want 0755", st.Mode().Perm())
	}
}

// TestApplyModeAlreadyCorrect ensures a no-op when the mode already matches.
func TestApplyModeAlreadyCorrect(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "d")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	res, err := Apply(Spec{
		Path:  target,
		Type:  TypeDir,
		Mode:  0o755,
		Owner: Owner{UID: -1, GID: -1},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.ChMod || res.Created {
		t.Errorf("expected no-op, got %+v", res)
	}
}

// TestApplyTypeMismatchFile rejects -f against an existing directory.
func TestApplyTypeMismatchFile(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "actually-dir")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_, err := Apply(Spec{Path: target, Type: TypeFile, Owner: Owner{UID: -1, GID: -1}})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("expected type mismatch error, got %v", err)
	}
}

// TestApplyTypeMismatchDir rejects -d against an existing file.
func TestApplyTypeMismatchDir(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "f")
	if err := os.WriteFile(target, []byte{'x'}, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Apply(Spec{Path: target, Type: TypeDir, Owner: Owner{UID: -1, GID: -1}})
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("expected type mismatch error, got %v", err)
	}
}

// TestApplyRefusesSymlink verifies we will not chmod/chown a symlink at the
// final component. This is a safety guarantee inherited from OpenRC.
func TestApplyRefusesSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "victim")
	link := filepath.Join(base, "link")
	if err := os.WriteFile(target, []byte{'x'}, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := Apply(Spec{Path: link, Type: TypeFile, Mode: 0o600, Owner: Owner{UID: -1, GID: -1}})
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("expected symlink refusal, got %v", err)
	}
}

// TestApplyDirectoryTruncate empties a non-empty directory when -D is set.
func TestApplyDirectoryTruncate(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "d")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Populate
	for _, name := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(target, name), []byte{'x'}, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	res, err := Apply(Spec{
		Path:     target,
		Type:     TypeDir,
		Truncate: true,
		Owner:    Owner{UID: -1, GID: -1},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.Truncated {
		t.Error("expected Truncated=true")
	}
	entries, _ := os.ReadDir(target)
	if len(entries) != 0 {
		t.Errorf("dir not empty: %v", entries)
	}
}

// TestApplyFileTruncate zeroes an existing file.
func TestApplyFileTruncate(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "f")
	if err := os.WriteFile(target, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := Apply(Spec{
		Path:     target,
		Type:     TypeFile,
		Truncate: true,
		Owner:    Owner{UID: -1, GID: -1},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	st, _ := os.Lstat(target)
	if st.Size() != 0 {
		t.Errorf("size = %d, want 0", st.Size())
	}
}

// TestApplyWritableExisting short-circuits success when the path is already
// writable and no creation flags are set.
func TestApplyWritableExisting(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "d")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	res, err := Apply(Spec{
		Path:     target,
		Writable: true,
		Owner:    Owner{UID: -1, GID: -1},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Created || res.ChMod {
		t.Errorf("expected no-op, got %+v", res)
	}
}

// TestApplyWritableMissingNoType: -W alone against a missing path is a hard
// error (no type to create).
func TestApplyWritableMissingNoType(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "nope")

	_, err := Apply(Spec{
		Path:     target,
		Writable: true,
		Owner:    Owner{UID: -1, GID: -1},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestApplyWritableMissingWithType: -W combined with -d creates the dir
// (matching OpenRC's fall-through behaviour).
func TestApplyWritableMissingWithType(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "new")

	res, err := Apply(Spec{
		Path:     target,
		Type:     TypeDir,
		Writable: true,
		Mode:     0o755,
		Owner:    Owner{UID: -1, GID: -1},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.Created {
		t.Error("expected Created=true via -W fall-through")
	}
}

// TestApplyOwnerNoChange exercises the UID/GID -1 path — no chown called.
func TestApplyOwnerNoChange(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "d")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	res, err := Apply(Spec{
		Path:  target,
		Type:  TypeDir,
		Owner: Owner{UID: -1, GID: -1},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.ChOwn {
		t.Error("expected ChOwn=false")
	}
}

// TestApplyChownCurrentUID sets owner to the current euid/egid, which must
// be a no-op (already matches).
func TestApplyChownCurrentUID(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "d")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	res, err := Apply(Spec{
		Path: target,
		Type: TypeDir,
		Owner: Owner{
			UID: os.Geteuid(),
			GID: os.Getegid(),
		},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.ChOwn {
		t.Error("expected ChOwn=false when owner already matches")
	}
}

// TestParseMode covers the happy paths.
func TestParseMode(t *testing.T) {
	cases := map[string]os.FileMode{
		"0755": 0o755,
		"755":  0o755,
		"0o644": 0o644,
		"0":    0,
	}
	for in, want := range cases {
		got, err := ParseMode(in)
		if err != nil {
			t.Errorf("ParseMode(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseMode(%q) = %o, want %o", in, got, want)
		}
	}
}

// TestParseModeBad rejects non-octal input.
func TestParseModeBad(t *testing.T) {
	for _, in := range []string{"", "abc", "0999"} {
		if _, err := ParseMode(in); err == nil {
			t.Errorf("ParseMode(%q) should have failed", in)
		}
	}
}

// TestParseOwnerEmpty leaves both sides unchanged.
func TestParseOwnerEmpty(t *testing.T) {
	own, err := ParseOwner("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if own.UID != -1 || own.GID != -1 {
		t.Errorf("expected {-1,-1}, got %+v", own)
	}
}

// TestParseOwnerNumeric parses "uid:gid" without hitting /etc/passwd.
func TestParseOwnerNumeric(t *testing.T) {
	own, err := ParseOwner("1234:5678")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if own.UID != 1234 || own.GID != 5678 {
		t.Errorf("got %+v", own)
	}
}

// TestParseOwnerUserOnly sets only UID, leaves GID == -1.
func TestParseOwnerUserOnly(t *testing.T) {
	own, err := ParseOwner("42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if own.UID != 42 || own.GID != -1 {
		t.Errorf("got %+v", own)
	}
}

// TestApplyAccessUsesUnix sanity-checks that unix.Access picks up a mode-0
// file as unwritable on Writable=true (the ENOENT fast-path test above
// leaves this branch uncovered).
func TestApplyAccessUsesUnix(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses mode bits")
	}
	base := t.TempDir()
	target := filepath.Join(base, "locked")
	if err := os.WriteFile(target, []byte{'x'}, 0o000); err != nil {
		t.Fatalf("write: %v", err)
	}

	// access() should fail with EACCES, not ENOENT → hard error.
	_, err := Apply(Spec{Path: target, Writable: true, Owner: Owner{UID: -1, GID: -1}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not writable") {
		t.Errorf("unexpected error: %v", err)
	}
	_ = unix.EACCES // keep unix imported even if the branch doesn't reference it
}
