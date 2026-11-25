package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Every type must resolve its parent without following a symlink that
// the user who owns it could point anywhere. base/link -> outside is
// such a link: a user's, with an absolute target, so following it steps
// from that user's files into root's "/" (an unsafe transition in
// systemd's CHASE_SAFE terms). Before, d created outside/newdir, z
// re-moded outside/victim and r deleted it.
func TestEntriesDoNotFollowParentSymlink(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("as root every link and directory here is root's, which the rule trusts")
	}
	for _, tc := range []struct {
		kind, leaf, arg string
		mode            uint32
	}{
		{kind: "d", leaf: "newdir", mode: 0o700},
		{kind: "D", leaf: "dir", mode: 0o700},
		{kind: "f", leaf: "newfile", mode: 0o644},
		{kind: "F", leaf: "victim", mode: 0o644, arg: "clobbered"},
		{kind: "w", leaf: "victim", arg: "clobbered"},
		{kind: "z", leaf: "victim", mode: 0o644},
		{kind: "r", leaf: "victim"},
		{kind: "R", leaf: "dir"},
		{kind: "L", leaf: "newlink", arg: "/etc"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			root := t.TempDir()
			base, outside := filepath.Join(root, "base"), filepath.Join(root, "outside")
			for _, d := range []string{base, filepath.Join(outside, "dir")} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			victim := filepath.Join(outside, "victim")
			if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			inDir := filepath.Join(outside, "dir", "keep")
			if err := os.WriteFile(inDir, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(base, "link")); err != nil {
				t.Fatal(err)
			}

			e := entry{kind: tc.kind, path: filepath.Join(base, "link", tc.leaf), mode: tc.mode, uid: -1, gid: -1, arg: tc.arg}
			if err := apply(e); err == nil {
				t.Errorf("%s %s: no error, want a refusal", tc.kind, e.path)
			}

			entries, _ := os.ReadDir(outside)
			if len(entries) != 2 {
				t.Errorf("%s created or removed entries in %s: %v", tc.kind, outside, entries)
			}
			if b, err := os.ReadFile(victim); err != nil || string(b) != "keep" {
				t.Errorf("%s reached %s: %q %v", tc.kind, victim, b, err)
			}
			if fi, err := os.Stat(victim); err == nil && fi.Mode().Perm() != 0o600 {
				t.Errorf("%s re-moded %s to %v", tc.kind, victim, fi.Mode().Perm())
			}
			if _, err := os.Stat(inDir); err != nil {
				t.Errorf("%s emptied %s: %v", tc.kind, filepath.Dir(inDir), err)
			}
		})
	}
}

// A link that stays within what its owner owns is still followed:
// base/rel -> real (relative, same owner) is the shape of a legitimate
// link such as /var/run -> /run, only not root's.
func TestEntriesFollowSafeParentSymlink(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(base, "rel")); err != nil {
		t.Fatal(err)
	}
	if err := apply(entry{kind: "d", path: filepath.Join(base, "rel", "new"), mode: 0o755, uid: -1, gid: -1}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(base, "real", "new")); err != nil || !fi.IsDir() {
		t.Errorf("d through a safe symlink did not create real/new: %v", err)
	}
}
