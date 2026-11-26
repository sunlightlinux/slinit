package dissect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubTools puts fake losetup/lsblk/mount/umount on PATH. Each
// appends its argv to dir/calls; losetup --find answers with a loop
// device and lsblk with fstype.
func stubTools(t *testing.T, fstype string) (calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls")
	stubs := map[string]string{
		"losetup": "case \"$1\" in --find) echo /dev/loopSTUB9;; esac",
		"lsblk":   "echo " + fstype,
		"mount":   "exit 0",
		"umount":  "exit 0",
	}
	for name, body := range stubs {
		script := "#!/bin/sh\necho \"" + name + " $*\" >> " + calls + "\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return calls
}

// TestMountROHardened: the image is untrusted — a setuid binary or a
// device node on it must not be usable through the mount.
func TestMountROHardened(t *testing.T) {
	calls := stubTools(t, "")
	if err := mountRO("/dev/loopSTUB9", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(calls)
	t.Logf("calls:\n%s", b)
	var opts string
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		for i := 0; i+1 < len(f); i++ {
			if f[0] == "mount" && f[i] == "-o" {
				opts = f[i+1]
			}
		}
	}
	got := map[string]bool{}
	for _, o := range strings.Split(opts, ",") {
		got[o] = true
	}
	for _, want := range []string{"ro", "nosuid", "nodev", "noexec"} {
		if !got[want] {
			t.Errorf("mount -o %q lacks %s", opts, want)
		}
	}
}

// TestAttachStrictRefusalCleansUp: --image-policy=strict refusing a
// LUKS device returned without detaching the loop device or removing
// the temporary mount point.
func TestAttachStrictRefusalCleansUp(t *testing.T) {
	calls := stubTools(t, "crypto_LUKS")
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	origEUID := geteuid
	geteuid = func() int { return 0 }
	defer func() { geteuid = origEUID }()

	img := filepath.Join(t.TempDir(), "disk.img")
	if err := os.WriteFile(img, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, detach, err := Attach(img, &Policy{Strict: true})
	if err == nil {
		detach()
		t.Fatal("strict policy accepted a LUKS device")
	}
	b, _ := os.ReadFile(calls)
	t.Logf("err=%v\ncalls:\n%s", err, b)
	if !strings.Contains(string(b), "losetup --detach /dev/loopSTUB9") {
		t.Error("loop device left attached after the strict refusal")
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "slinit-dissect-*")); len(left) != 0 {
		t.Errorf("temporary mount point left behind: %v", left)
	}
}

// The journal directory was probed with os.Stat on the mount point plus
// the subpath, which follows the image's symlinks on the host: an image
// whose var/log/slinit-journal pointed at a host directory had that
// directory's journal shown as the image's.
func TestAttachResolvesJournalDirInsideImage(t *testing.T) {
	calls := stubTools(t, "")
	host := t.TempDir()
	if err := os.WriteFile(filepath.Join(host, "host.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mount := "#!/bin/sh\necho \"mount $*\" >> " + calls + "\nmkdir -p \"$4/var/log\" && ln -s " + host + " \"$4/var/log/slinit-journal\"\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(calls), "mount"), []byte(mount), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", t.TempDir())
	origEUID := geteuid
	geteuid = func() int { return 0 }
	defer func() { geteuid = origEUID }()
	img := filepath.Join(t.TempDir(), "disk.img")
	if err := os.WriteFile(img, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	mnt, jdir, detach, err := Attach(img, nil)
	if err != nil {
		return // no journal inside the image: right
	}
	defer detach()
	real, _ := filepath.EvalSymlinks(jdir)
	if !strings.HasPrefix(real, mnt+"/") {
		t.Fatalf("journal dir %s resolves to %s, outside the image mounted at %s", jdir, real, mnt)
	}
}

// A journal directory that is in the image is found, its symlinks
// resolved inside it.
func TestAttachFindsJournalDirInsideImage(t *testing.T) {
	calls := stubTools(t, "")
	mount := "#!/bin/sh\necho \"mount $*\" >> " + calls + "\nmkdir -p \"$4/data/j\" \"$4/var/log\" && ln -s /data/j \"$4/var/log/slinit-journal\"\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(calls), "mount"), []byte(mount), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", t.TempDir())
	origEUID := geteuid
	geteuid = func() int { return 0 }
	defer func() { geteuid = origEUID }()
	img := filepath.Join(t.TempDir(), "disk.img")
	if err := os.WriteFile(img, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	mnt, jdir, detach, err := Attach(img, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer detach()
	real, _ := filepath.EvalSymlinks(mnt)
	if jdir != filepath.Join(real, "data/j") {
		t.Errorf("journal dir = %s; want %s", jdir, filepath.Join(real, "data/j"))
	}
}
