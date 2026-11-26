package autofs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestParseMountOptionsUserspaceDropped(t *testing.T) {
	// fstab-only options went to the filesystem as data, and the
	// filesystem refused the mount with EINVAL.
	flags, data := parseMountFlags("defaults,auto,noauto,nofail,_netdev,user,nouser,users,owner,group," +
		"comment=systemd.automount,x-systemd.idle-timeout=5,X-mount.mkdir,size=1m")
	if flags != 0 {
		t.Errorf("flags = %#x; want 0", flags)
	}
	if data != "size=1m" {
		t.Errorf("data = %q; want %q", data, "size=1m")
	}
}

func TestParseMountOptionsClearing(t *testing.T) {
	for _, tc := range []struct {
		opts string
		want uintptr
	}{
		{"ro,rw", 0},
		{"rw,ro", unix.MS_RDONLY},
		{"nosuid,suid", 0},
		{"nodev,dev", 0},
		{"noexec,exec", 0},
		{"exec,noexec", unix.MS_NOEXEC},
		{"sync,async", 0},
		{"mand,nomand", 0},
		{"lazytime,nolazytime", 0},
		{"nosymfollow", unix.MS_NOSYMFOLLOW},
		{"nosymfollow,symfollow", 0},
		{"noatime,relatime", unix.MS_RELATIME},
		{"relatime,noatime", unix.MS_NOATIME},
		{"noatime,strictatime", unix.MS_STRICTATIME},
		{"noatime,atime", 0},
		{"nodiratime,diratime", 0},
		{"silent,loud", 0},
		{"rbind,ro", unix.MS_BIND | unix.MS_REC | unix.MS_RDONLY},
	} {
		flags, data := parseMountFlags(tc.opts)
		if flags != tc.want || data != "" {
			t.Errorf("parseMountFlags(%q) = %#x, %q; want %#x, \"\"", tc.opts, flags, data, tc.want)
		}
	}
}

// inMountNS re-runs the calling test as root in a private mount
// namespace (a user namespace too when not root), after setup — a shell
// script given the test's scratch directory as $BASE — has run there.
// With lock set the test runs one user namespace further down, where the
// kernel locks the flags of every mount setup made. It returns true in
// the re-run, false in the caller, which then returns.
func inMountNS(t *testing.T, setup string, lock bool) bool {
	t.Helper()
	if os.Getenv("SLINIT_TEST_MOUNTNS") != "" {
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

func statfsFlags(t *testing.T, path string) int64 {
	t.Helper()
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		t.Fatalf("statfs %s: %v", path, err)
	}
	return st.Flags
}

func isMountPoint(t *testing.T, path string) bool {
	t.Helper()
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if f := strings.Fields(line); len(f) > 4 && f[4] == path {
			return true
		}
	}
	return false
}

const bindSetup = `mount -t tmpfs tmpfs "$BASE"
mkdir "$BASE/src" "$BASE/dst"
mount -t tmpfs -o nosuid tmpfs "$BASE/src"`

// bind,ro,nosuid,nodev,noexec used to be one mount(2) call with
// MS_BIND|flags; the kernel ignores the flags on a bind, so the mount was
// writable and exec.
func TestBindMountAppliesFlags(t *testing.T) {
	if !inMountNS(t, bindSetup, false) {
		return
	}
	base := os.Getenv("SLINIT_TEST_MOUNTNS")
	src, dst := filepath.Join(base, "src"), filepath.Join(base, "dst")
	for _, opts := range []string{"bind,ro,nosuid,nodev,noexec", "rbind,ro,nodev,noexec,nosymfollow"} {
		if err := mountWithOptions(src, dst, "", opts); err != nil {
			t.Fatalf("mount %s: %v", opts, err)
		}
		got := statfsFlags(t, dst)
		want := int64(unix.ST_RDONLY | unix.ST_NOSUID | unix.ST_NODEV | unix.ST_NOEXEC)
		if strings.Contains(opts, "nosymfollow") {
			want |= stNoSymfollow
		}
		if got&want != want {
			t.Errorf("%s: statfs flags %#x; want %#x set", opts, got, want)
		}
		if err := os.WriteFile(filepath.Join(dst, "f"), nil, 0o644); err == nil {
			t.Errorf("%s: bind mount is writable", opts)
		}
		if statfsFlags(t, src)&unix.ST_RDONLY != 0 {
			t.Errorf("%s: source turned read-only", opts)
		}
		if err := unix.Unmount(dst, unix.MNT_DETACH); err != nil {
			t.Fatal(err)
		}
	}
}

// The source's nosuid is locked in the inner user namespace. bind,ro must
// keep it (and not fail); bind,suid cannot drop it, so the mount fails
// and the bind is not left behind, suid-less and writable.
func TestBindMountLockedFlags(t *testing.T) {
	if !inMountNS(t, bindSetup, true) {
		return
	}
	base := os.Getenv("SLINIT_TEST_MOUNTNS")
	src, dst := filepath.Join(base, "src"), filepath.Join(base, "dst")
	if err := mountWithOptions(src, dst, "", "bind,ro"); err != nil {
		t.Fatalf("bind,ro: %v", err)
	}
	if got := statfsFlags(t, dst); got&(unix.ST_RDONLY|unix.ST_NOSUID) != unix.ST_RDONLY|unix.ST_NOSUID {
		t.Errorf("bind,ro: statfs flags %#x; want ro,nosuid", got)
	}
	if err := unix.Unmount(dst, unix.MNT_DETACH); err != nil {
		t.Fatal(err)
	}
	if err := mountWithOptions(src, dst, "", "bind,ro,suid"); err == nil {
		t.Errorf("bind,ro,suid of a locked nosuid mount succeeded")
	}
	if isMountPoint(t, dst) {
		t.Errorf("failed bind left a mount at %s", dst)
	}
}
