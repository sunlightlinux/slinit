package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// inMountNS re-runs the calling test as root in a private mount
// namespace (and, when not root, a user namespace mapping the caller to
// root), after setup — a shell script, given the test's scratch
// directory as $BASE — has built the mounts it needs there. With lock
// set the test runs one user namespace further down, where the kernel
// locks the flags of every mount setup made, as it does in an
// unprivileged container. It returns true in the re-run, where the test
// goes on, and false in the caller, which then returns.
func inMountNS(t *testing.T, setup string, lock bool) bool {
	t.Helper()
	if base := os.Getenv("SLINIT_TEST_MOUNTNS"); base != "" {
		if os.Geteuid() != 0 {
			t.Skip("not root inside the namespace")
		}
		return true
	}
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skip("unshare not available")
	}
	outer := []string{"--mount"}
	if os.Geteuid() != 0 {
		outer = []string{"--user", "--map-root-user", "--mount"}
	}
	if exec.Command(unshare, append(outer, "true")...).Run() != nil {
		t.Skipf("unshare %s not permitted here", strings.Join(outer, " "))
	}
	inner := "exec \"$@\""
	if lock {
		inner = "exec unshare --user --map-root-user --mount \"$@\""
	}
	base := t.TempDir()
	args := append(outer, "sh", "-c", "set -e\n"+setup+"\n"+inner, "sh",
		os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v", "-test.count=1")
	cmd := exec.Command(unshare, args...)
	cmd.Env = append(os.Environ(), "SLINIT_TEST_MOUNTNS="+base, "BASE="+base)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("in mount namespace: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "--- SKIP") {
		t.Skipf("skipped in mount namespace:\n%s", out)
	}
	return false
}

func mountFlags(t *testing.T, path string) int64 {
	t.Helper()
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		t.Fatalf("statfs %s: %v", path, err)
	}
	return st.Flags
}

func wantRO(t *testing.T, path string, ro bool) {
	t.Helper()
	if got := mountFlags(t, path)&unix.ST_RDONLY != 0; got != ro {
		t.Errorf("%s read-only = %v; want %v", path, got, ro)
	}
}

// The read-only remount used to act on the one mount at the path: a
// recursive bind brought the submounts along, writable as they were.
// read-only-paths=$BASE left $BASE/sub writable.
func TestReadOnlyPathCoversSubmounts(t *testing.T) {
	if !inMountNS(t, `mount -t tmpfs tmpfs "$BASE"
mkdir "$BASE/sub"
mount -t tmpfs tmpfs "$BASE/sub"
mkdir "$BASE/sub/deeper"
mount -t tmpfs tmpfs "$BASE/sub/deeper"`, false) {
		return
	}
	base := os.Getenv("BASE")
	if err := bindMount(base, base, true); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{base, base + "/sub", base + "/sub/deeper"} {
		wantRO(t, p, true)
	}
}

// protect-home=read-only left /run/user/UID, a mount of its own on any
// system with a logged-in user, writable.
func TestProtectHomeReadOnlyCoversRunUser(t *testing.T) {
	if !inMountNS(t, `mount -t tmpfs tmpfs /home
mkdir /home/alice
mount -t tmpfs tmpfs /home/alice
mkdir -p /run/user
mount -t tmpfs tmpfs /run/user
mkdir /run/user/1000
mount -t tmpfs tmpfs /run/user/1000`, false) {
		return
	}
	if err := applyProtectHome("read-only"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/home", "/home/alice", "/run/user", "/run/user/1000"} {
		wantRO(t, p, true)
	}
}

// protect-system=strict made / read-only and nothing mounted below it;
// the documented exceptions (/dev, /proc, /sys, /run, /tmp, /var/tmp)
// must stay writable, and so must a read-write-path's submounts.
func TestProtectSystemStrictCoversSubmounts(t *testing.T) {
	if _, err := os.Stat("/mnt"); err != nil {
		t.Skip("no /mnt to mount a fixture on")
	}
	if !inMountNS(t, `mount -t tmpfs tmpfs /mnt
mkdir /mnt/sub /mnt/rw
mount -t tmpfs tmpfs /mnt/sub
mount -t tmpfs tmpfs /mnt/rw
mkdir /mnt/rw/data
mount -t tmpfs tmpfs /mnt/rw/data
mount -t tmpfs tmpfs "$BASE"`, false) {
		return
	}
	base := os.Getenv("BASE") // under /tmp
	if err := applyProtectSystem("strict", nil); err != nil {
		t.Fatal(err)
	}
	if err := bindMount("/mnt/rw", "/mnt/rw", false); err != nil {
		t.Fatal(err)
	}
	wantRO(t, "/", true)
	wantRO(t, "/mnt", true)
	wantRO(t, "/mnt/sub", true)
	wantRO(t, "/mnt/rw", false)
	wantRO(t, "/mnt/rw/data", false)
	wantRO(t, base, false)
	for _, p := range []string{"/dev", "/proc", "/sys", "/run"} {
		if mountFlags(t, p)&unix.ST_RDONLY != 0 {
			continue // read-only on the host already
		}
		wantRO(t, p, false)
	}
}

// The read-only remount passed MS_RDONLY alone, and a bind remount
// replaces the per-mount flags: a noexec, nosuid or nodev mount made
// read-only lost them, so a script on a noexec tmpfs listed in
// read-only-paths could be run.
func TestReadOnlyPathKeepsMountFlags(t *testing.T) {
	if !inMountNS(t, `mount -t tmpfs -o nosuid,nodev,noexec tmpfs "$BASE"
mkdir "$BASE/sub"
mount -t tmpfs -o nosuid,noexec,noatime tmpfs "$BASE/sub"`, false) {
		return
	}
	base := os.Getenv("BASE")
	if err := bindMount(base, base, true); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]int64{
		base:          unix.ST_RDONLY | unix.ST_NOSUID | unix.ST_NODEV | unix.ST_NOEXEC,
		base + "/sub": unix.ST_RDONLY | unix.ST_NOSUID | unix.ST_NOEXEC | unix.ST_NOATIME,
	} {
		if got := mountFlags(t, p); got&want != want {
			t.Errorf("%s flags %#x; want %#x set", p, got, want)
		}
	}
}

// In a user namespace below the one that made a mount, as in an
// unprivileged container, the kernel refuses a remount that would clear
// its nosuid, nodev, noexec or atime flags: dropping them made every
// read-only remount there fail with EPERM, and the service did not start.
func TestReadOnlyPathLockedMountFlags(t *testing.T) {
	if !inMountNS(t, `mount -t tmpfs -o nosuid,nodev,noexec tmpfs "$BASE"
mkdir "$BASE/sub"
mount -t tmpfs -o nosuid,nodev tmpfs "$BASE/sub"`, true) {
		return
	}
	base := os.Getenv("BASE")
	if err := remountRO(base); err != nil {
		t.Fatal(err)
	}
	wantRO(t, base, true)
	wantRO(t, base+"/sub", true)
}

// protect-kernel-tunables remounted /proc/sys read-only and left
// /proc/sys/fs/binfmt_misc, a mount of its own, writable: the service
// could register an interpreter for every binary on the system. In a
// user namespace /proc's flags are locked, so this also needs the
// remount to keep them.
func TestProtectKernelTunablesCoversBinfmtMisc(t *testing.T) {
	if !inMountNS(t, `mount -t binfmt_misc binfmt_misc /proc/sys/fs/binfmt_misc 2>/dev/null || true`, false) {
		return
	}
	var st unix.Statfs_t
	if unix.Statfs("/proc/sys/fs/binfmt_misc", &st) != nil || st.Type != 0x42494e4d {
		t.Skip("binfmt_misc cannot be mounted here")
	}
	if err := remountROIfMount("/proc/sys"); err != nil {
		t.Fatal(err)
	}
	wantRO(t, "/proc/sys", true)
	wantRO(t, "/proc/sys/fs/binfmt_misc", true)
}
