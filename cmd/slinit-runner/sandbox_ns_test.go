package main

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
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

// protect-home=tmpfs mounted writable tmpfs over /home, /root and
// /run/user, kept writable under protect-system: a service could fill
// them, or leave files there for itself. systemd mounts them read-only,
// /root with mode 0700.
func TestProtectHomeTmpfsReadOnly(t *testing.T) {
	if !inMountNS(t, `mount -t tmpfs tmpfs /home
mount -t tmpfs tmpfs /run
mkdir /run/user`, false) {
		return
	}
	if err := applySandbox(sandboxSpec{protectHome: "tmpfs", protectSystem: "strict"}); err != nil {
		t.Fatal(err)
	}
	for p, mode := range map[string]os.FileMode{"/home": 0o755, "/root": 0o700, "/run/user": 0o755} {
		wantRO(t, p, true)
		if st, err := os.Stat(p); err != nil {
			t.Error(err)
		} else if st.Mode().Perm() != mode {
			t.Errorf("%s mode %v; want %v", p, st.Mode().Perm(), mode)
		}
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

// A read-write-path below a read-only-path is a hole in it, as in
// systemd, where the deeper path wins. read-only-paths are applied after
// read-write-paths, and once the read-only remount covered submounts it
// turned the hole read-only too: read-only-paths=/etc with
// read-write-paths=/etc/x left /etc/x read-only.
func TestReadWritePathInsideReadOnlyPath(t *testing.T) {
	if !inMountNS(t, `mount -t tmpfs tmpfs "$BASE"
mkdir "$BASE/rw" "$BASE/ro" "$BASE/sub"
mount -t tmpfs tmpfs "$BASE/sub"`, false) {
		return
	}
	base := os.Getenv("BASE")
	// "/" is listed too: a read-write path above the read-only one is
	// not a hole in it and must not keep anything below it writable.
	err := applySandbox(sandboxSpec{
		readOnlyPaths:  []string{base},
		readWritePaths: []string{"/", base + "/rw"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantRO(t, base, true)
	wantRO(t, base+"/sub", true)
	wantRO(t, base+"/rw", false)
	if err := os.WriteFile(base+"/rw/f", nil, 0o600); err != nil {
		t.Errorf("write in the read-write hole: %v", err)
	}
	if err := os.WriteFile(base+"/ro/f", nil, 0o600); err == nil {
		t.Errorf("write beside the hole succeeded; %s should be read-only", base)
	}
}

// A read-write-path turned back every mount below it that any directive
// had made read-only: protect-home=read-only with read-write-paths=/run
// left /run/user/1000 writable, and a bind-read-only-path below a
// read-write-path became writable. In systemd the deeper path wins, and
// of two on the same path the read-only one.
func TestReadWritePathKeepsDeeperReadOnly(t *testing.T) {
	if _, err := os.Stat("/mnt"); err != nil {
		t.Skip("no /mnt to mount a fixture on")
	}
	if !inMountNS(t, `mount -t tmpfs tmpfs /run
mkdir -p /run/user/1000
mount -t tmpfs tmpfs /run/user/1000
mount -t tmpfs tmpfs /home
mkdir /home/alice
mount -t tmpfs tmpfs /mnt
mkdir -p /mnt/src /mnt/app/conf`, false) {
		return
	}
	err := applySandbox(sandboxSpec{
		protectHome:    "read-only",
		bindROPaths:    []string{"/mnt/src:/mnt/app/conf"},
		readWritePaths: []string{"/run", "/mnt/app", "/home", "/home/alice"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantRO(t, "/run", false)
	wantRO(t, "/run/user", true)
	wantRO(t, "/run/user/1000", true)
	wantRO(t, "/mnt/app", false)
	wantRO(t, "/mnt/app/conf", true)
	wantRO(t, "/home", true)        // same path: read-only wins
	wantRO(t, "/home/alice", false) // deeper: read-write wins
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

// protect-hostname refused sethostname and setdomainname but left
// /proc/sys/kernel/hostname and domainname writable, which set the same
// names; systemd's ProtectHostname= makes both read-only.
func TestProtectHostnameReadOnlySysctl(t *testing.T) {
	if !inMountNS(t, "", false) {
		return
	}
	runtime.LockOSThread() // the call filter is per thread; see below
	if err := applyHardening(hardeningSpec{protectHostname: true}); err != nil {
		t.Fatal(err)
	}
	wantRO(t, "/proc/sys/kernel/hostname", true)
	wantRO(t, "/proc/sys/kernel/domainname", true)
}

// protect-kernel-tunables, -modules, -logs and protect-clock only
// filtered system calls (and made /proc/sys and /dev/kmsg safe), while
// systemd's also make /sys and the rest of /proc's kernel knobs
// read-only, hide /proc/kallsyms, /proc/kcore, /proc/kmsg and
// /usr/lib/modules, and take CAP_SYS_MODULE, CAP_SYSLOG, CAP_SYS_TIME and
// CAP_WAKE_ALARM out of the bounding set.
func TestProtectKernelMatchesSystemd(t *testing.T) {
	if !inMountNS(t, "", false) {
		return
	}
	cgroupRO := mountFlags(t, "/sys/fs/cgroup")&unix.ST_RDONLY != 0
	// The bounding set is per thread, as in the runner, which execs
	// from the thread that dropped it; this one is discarded when the
	// test returns still locked to it.
	runtime.LockOSThread()
	if err := applyHardening(hardeningSpec{
		protectKernelTunables: true, protectKernelModules: true,
		protectKernelLogs: true, protectClock: true,
	}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/proc/sys", "/proc/sysrq-trigger", "/proc/irq", "/proc/bus", "/proc/fs", "/sys", "/sys/kernel/security"} {
		if _, err := os.Stat(p); err == nil {
			wantRO(t, p, true)
		}
	}
	wantRO(t, "/sys/fs/cgroup", cgroupRO) // protect-control-groups' business
	for _, p := range []string{"/proc/kallsyms", "/proc/kcore", "/proc/kmsg"} {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
			t.Errorf("%s readable: %d bytes", p, len(b))
		}
	}
	if _, err := os.Stat("/usr/lib/modules"); err == nil {
		if ents, err := os.ReadDir("/usr/lib/modules"); err == nil && len(ents) > 0 {
			t.Errorf("/usr/lib/modules still lists %d entries", len(ents))
		}
	}
	status, err := os.ReadFile("/proc/thread-self/status")
	if err != nil {
		t.Fatal(err)
	}
	var bnd uint64
	for _, line := range strings.Split(string(status), "\n") {
		if v, ok := strings.CutPrefix(line, "CapBnd:"); ok {
			bnd, _ = strconv.ParseUint(strings.TrimSpace(v), 16, 64)
		}
	}
	for name, c := range map[string]uint{"CAP_SYS_MODULE": 16, "CAP_SYS_TIME": 25, "CAP_SYSLOG": 34, "CAP_WAKE_ALARM": 35} {
		if bnd&(1<<c) != 0 {
			t.Errorf("%s still in the bounding set (%#x)", name, bnd)
		}
	}
}
