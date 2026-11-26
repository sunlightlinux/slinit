package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

// inMountNS re-runs the calling test as root of a new user and mount
// namespace (`unshare -Urm`), where it may mount over /run without
// touching the host, and reports whether this is the re-run. The parent
// fails if the re-run does.
func inMountNS(t *testing.T) bool {
	t.Helper()
	if os.Getenv("SLINIT_LOGIND_TEST_NS") == t.Name() {
		return true
	}
	if testing.Short() {
		t.Skip("namespace test skipped under -short")
	}
	// Probe a mount, not just the unshare: under AppArmor's userns
	// restriction the namespace is made but holds no capabilities.
	if err := exec.Command("unshare", "-Urm", "mount", "-t", "tmpfs", "none", "/run").Run(); err != nil {
		t.Skipf("cannot mount in a user namespace here: %v", err)
	}
	cmd := exec.Command("unshare", "-Urm", os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), "SLINIT_LOGIND_TEST_NS="+t.Name())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("in the namespace: %v\n%s", err, out)
	}
	t.Logf("in the namespace:\n%s", out)
	return false
}

// nsMount mounts or fails the test.
func nsMount(t *testing.T, src, dst, fstype string, flags uintptr, data string) {
	t.Helper()
	if err := unix.Mount(src, dst, fstype, flags, data); err != nil {
		t.Fatalf("mount %s on %s: %v", fstype, dst, err)
	}
}

// fill writes into dir until the filesystem refuses.
func fill(t *testing.T, dir string) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, "hog"))
	if err != nil {
		t.Fatalf("create hog: %v", err)
	}
	defer f.Close()
	buf := make([]byte, 64<<10)
	for {
		if _, err := f.Write(buf); err != nil {
			if !errors.Is(err, unix.ENOSPC) {
				t.Fatalf("filling %s: %v", dir, err)
			}
			return
		}
	}
}

func freeBytes(t *testing.T, path string) uint64 {
	t.Helper()
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		t.Fatal(err)
	}
	return st.Bavail * uint64(st.Bsize)
}

// nsLogind sets up, in the namespace, a 16 MiB /run, a writable
// /sys/fs/cgroup and 40 MiB of "RAM" (so a 4 MiB runtime directory), and
// returns a manager that takes root's calls.
func nsLogind(t *testing.T) *manager {
	t.Helper()
	meminfo := filepath.Join(t.TempDir(), "meminfo")
	if err := os.WriteFile(meminfo, []byte("MemTotal:       40960 kB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nsMount(t, meminfo, "/proc/meminfo", "", unix.MS_BIND, "")
	nsMount(t, "tmpfs", "/run", "tmpfs", 0, "size=16m,mode=0755")
	nsMount(t, "tmpfs", "/sys/fs/cgroup", "tmpfs", 0, "")
	stateRoot = "/run/slinit-logind"
	for _, d := range []string{"sessions", "users"} {
		if err := os.MkdirAll(filepath.Join(stateRoot, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fakeBus(t, map[dbus.Sender]uint32{root: 0})
	return &manager{inhibitors: newInhibitRegistry()}
}

// A user's runtime directory was a plain directory on /run, so anyone who
// could log in could fill /run, and with it every pid file, socket and
// credentials directory PID 1 and the system services make there. Like
// systemd-logind, slinit-logind now mounts a tmpfs of its own there,
// capped at 10% of RAM. It is kept after the last logout (no linger in
// slinit-logind: a user instance may outlive the sessions).
func TestRuntimeDirIsOwnTmpfs(t *testing.T) {
	if !inMountNS(t) {
		return
	}
	m := nsLogind(t)

	// Only uid 0 is mapped in the namespace.
	const uid = 0
	id, _, rt, _, _, _, _, _, derr := m.CreateSession(root, uid, uint32(os.Getpid()),
		"login", "tty", "user", "", "", 0, "", "", false, "", "", nil)
	if derr != nil {
		t.Fatalf("CreateSession: %v", derr)
	}
	if rt != "/run/user/0" {
		t.Fatalf("runtime path %q", rt)
	}
	fi, err := os.Stat(rt)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("runtime dir mode %o, want 700", fi.Mode().Perm())
	}

	before := freeBytes(t, "/run")
	fill(t, rt)
	used := before - freeBytes(t, "/run")
	if used > 1<<20 {
		t.Errorf("filling %s used %d KiB of /run (free before: %d KiB): the user can fill the filesystem PID 1 keeps its sockets on",
			rt, used>>10, before>>10)
	}
	if got := freeBytes(t, rt) + uint64(fileSize(t, filepath.Join(rt, "hog"))); got > 5<<20 {
		t.Errorf("runtime dir holds %d KiB, want it capped near 10%% of RAM (4 MiB)", got>>10)
	}

	if derr := m.releaseSession(id); derr != nil {
		t.Fatalf("releaseSession: %v", derr)
	}
	if !isMountPoint(rt) {
		t.Errorf("runtime dir is no longer its own tmpfs after the last session ended")
	}
	if free := freeBytes(t, "/run"); free < before-(256<<10) {
		t.Errorf("/run free %d KiB after the last logout, %d KiB before the user filled it", free>>10, before>>10)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

// A session open before this slinit-logind keeps its sockets in a plain
// runtime directory; a later session of the same user must not mount
// over it. Nor may ending one of two sessions take the directory away.
func TestRuntimeDirKeptForOpenSessions(t *testing.T) {
	if !inMountNS(t) {
		return
	}
	m := nsLogind(t)
	// The session from before the upgrade: a user record and a plain
	// directory with the user's bus socket in it.
	if err := writeJSONAtomic(sessionFile("c7"), SessionRecord{ID: "c7", UserID: 0}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(userFile(0), UserRecord{UID: 0}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("/run/user/0", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/run/user/0/bus", nil, 0o600); err != nil {
		t.Fatal(err)
	}

	id, _, rt, _, _, _, _, _, derr := m.CreateSession(root, 0, uint32(os.Getpid()),
		"login", "tty", "user", "", "", 0, "", "", false, "", "", nil)
	if derr != nil {
		t.Fatalf("CreateSession: %v", derr)
	}
	if isMountPoint(rt) {
		t.Errorf("%s was mounted over while a session of the user was open", rt)
	}
	if _, err := os.Stat(filepath.Join(rt, "bus")); err != nil {
		t.Errorf("the open session's bus socket is gone: %v", err)
	}
	if derr := m.releaseSession(id); derr != nil {
		t.Fatalf("releaseSession: %v", derr)
	}
	if _, err := os.Stat(filepath.Join(rt, "bus")); err != nil {
		t.Errorf("ending one of two sessions removed the runtime dir: %v", err)
	}
	if derr := m.releaseSession("c7"); derr != nil {
		t.Fatalf("releaseSession c7: %v", derr)
	}
	if _, err := os.Stat(filepath.Join(rt, "bus")); err != nil {
		t.Errorf("the last logout removed the runtime dir a user instance may still use: %v", err)
	}
}

// A user instance that outlives its sessions (turnstile, slinit -u) keeps
// its sockets in the runtime directory with no session open. A first
// session must not mount a tmpfs over them.
func TestRuntimeDirNotMountedOverContent(t *testing.T) {
	if !inMountNS(t) {
		return
	}
	m := nsLogind(t)
	if err := os.MkdirAll("/run/user/0", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/run/user/0/slinit.sock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, rt, _, _, _, _, _, derr := m.CreateSession(root, 0, uint32(os.Getpid()),
		"login", "tty", "user", "", "", 0, "", "", false, "", "", nil)
	if derr != nil {
		t.Fatalf("CreateSession: %v", derr)
	}
	if isMountPoint(rt) {
		t.Errorf("%s was mounted over its existing content", rt)
	}
	if _, err := os.Stat(filepath.Join(rt, "slinit.sock")); err != nil {
		t.Errorf("the user instance's socket is hidden: %v", err)
	}
}

// Without the right to mount (a container), the runtime directory is the
// plain one it always was.
func TestRuntimeDirFallsBackToPlainDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may be able to mount")
	}
	dir := filepath.Join(t.TempDir(), "1000")
	makeRuntimeDir(dir, uint32(os.Getuid()), true)
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("no runtime dir: %v", err)
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Errorf("runtime dir mode %v, want a 0700 directory", fi.Mode())
	}
	if isMountPoint(dir) {
		t.Error("runtime dir is a mount point without the right to mount")
	}
}
