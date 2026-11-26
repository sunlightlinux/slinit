package checkpath

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/internal/safepath"
)

// Apply resolved parents by path, so base/link/made with link -> outside
// created outside/made. The link is a user's with an absolute target,
// which systemd's CHASE_SAFE refuses (see internal/safepath).
func TestApplyDoesNotFollowParentSymlink(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("as root every link and directory here is root's, which the rule trusts")
	}
	for _, typ := range []PathType{TypeDir, TypeFile, TypeFifo} {
		root := t.TempDir()
		base, outside := filepath.Join(root, "base"), filepath.Join(root, "outside")
		for _, d := range []string{base, outside} {
			if err := os.Mkdir(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		victim := filepath.Join(outside, "victim")
		if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(base, "link")); err != nil {
			t.Fatal(err)
		}

		if _, err := Apply(Spec{Path: filepath.Join(base, "link", "made"), Type: typ, Owner: Owner{-1, -1}}); err == nil {
			t.Errorf("type %d through base/link: no error, want a refusal", typ)
		}
		if _, err := os.Lstat(filepath.Join(outside, "made")); err == nil {
			t.Errorf("type %d created %s through the symlink", typ, filepath.Join(outside, "made"))
		}
		_, err := Apply(Spec{Path: filepath.Join(base, "link", "victim"), Type: TypeFile, Mode: 0o644, Truncate: true, Owner: Owner{-1, -1}})
		if err == nil {
			t.Error("-F -m 0644 through base/link: no error, want a refusal")
		}
		if b, _ := os.ReadFile(victim); string(b) != "keep" {
			t.Errorf("victim truncated through the symlink: %q", b)
		}
		if fi, _ := os.Stat(victim); fi.Mode().Perm() != 0o600 {
			t.Errorf("victim re-moded through the symlink: %v", fi.Mode().Perm())
		}
	}
}

// A same-owner link with a relative target is still followed.
func TestApplyFollowsSafeParentSymlink(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(base, "rel")); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(Spec{Path: filepath.Join(base, "rel", "made"), Type: TypeDir, Owner: Owner{-1, -1}}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(base, "real", "made")); err != nil || !fi.IsDir() {
		t.Errorf("real/made not created: %v", err)
	}
}

// A hard-linked regular file is not changed while fs.protected_hardlinks
// is off, as OpenRC refuses it; with it on, it is.
func TestApplyRefusesHardlinkUnlessProtected(t *testing.T) {
	saved := safepath.HardlinksProtected
	t.Cleanup(func() { safepath.HardlinksProtected = saved })

	dir := t.TempDir()
	victim, link := filepath.Join(dir, "victim"), filepath.Join(dir, "link")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(victim, link); err != nil {
		t.Fatal(err)
	}

	safepath.HardlinksProtected = func() bool { return false }
	if _, err := Apply(Spec{Path: link, Type: TypeFile, Mode: 0o644, Truncate: true, Owner: Owner{-1, -1}}); err == nil {
		t.Error("hard link with protected_hardlinks off: no error, want a refusal")
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Errorf("victim truncated through the hard link: %q", b)
	}
	if fi, _ := os.Stat(victim); fi.Mode().Perm() != 0o600 {
		t.Errorf("victim re-moded through the hard link: %v", fi.Mode().Perm())
	}
	// Only checking that it exists changes nothing and is allowed.
	if _, err := Apply(Spec{Path: link, Type: TypeFile, Owner: Owner{-1, -1}}); err != nil {
		t.Errorf("plain check of a hard link: %v", err)
	}

	safepath.HardlinksProtected = func() bool { return true }
	if _, err := Apply(Spec{Path: link, Type: TypeFile, Mode: 0o644, Owner: Owner{-1, -1}}); err != nil {
		t.Errorf("hard link with protected_hardlinks on: %v", err)
	}
}
