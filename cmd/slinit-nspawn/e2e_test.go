package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// buildNspawn compiles slinit-nspawn for the end-to-end tests.
func buildNspawn(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "slinit-nspawn")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("building slinit-nspawn: %v\n%s", err, out)
	}
	return bin
}

// runInUserns runs script with sh inside `unshare -Urm` (root in a
// user + mount namespace, which is all slinit-nspawn needs), after
// building a rootfs under dir/rootfs from bind mounts of the host's
// /usr, /bin, /lib... so /bin/sh works as the container's init. The
// script sees $NSPAWN, $ROOTFS and $DIR. Skips when user namespaces
// are not available.
func runInUserns(t *testing.T, dir, script string) (string, int) {
	t.Helper()
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skip("unshare not available")
	}
	if exec.Command(unshare, "-Urm", "true").Run() != nil {
		t.Skip("cannot create a user + mount namespace here")
	}
	bin := buildNspawn(t)
	prelude := `set -u
mkdir -p "$ROOTFS"
for d in usr bin lib lib64 sbin; do
	if [ -L /$d ]; then ln -s "$(readlink /$d)" "$ROOTFS/$d"
	elif [ -d /$d ]; then mkdir -p "$ROOTFS/$d" && mount --rbind /$d "$ROOTFS/$d"
	fi
done
`
	cmd := exec.Command(unshare, "-Urm", "sh", "-c", prelude+script)
	cmd.Env = append(os.Environ(), "NSPAWN="+bin, "ROOTFS="+filepath.Join(dir, "rootfs"), "DIR="+dir)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("unshare: %v", err)
	}
	return strings.TrimSpace(string(out)), code
}

// TestContainerExitStatusUnregisters: the launcher exits with the
// container's status. It did so with os.Exit inside runParent, which
// skipped the deferred registry cleanup: every container that exited
// non-zero left a stale /run/slinit/machines entry behind.
func TestContainerExitStatusUnregisters(t *testing.T) {
	dir := t.TempDir()
	out, code := runInUserns(t, dir, `mkdir -p "$DIR/machines"
exec "$NSPAWN" --name t1 --boot "$ROOTFS" --machine-dir "$DIR/machines" --init /bin/sh -- -c 'exit 3'
`)
	t.Logf("exit=%d output:\n%s", code, out)
	if code != 3 {
		t.Errorf("launcher exited %d, want the container's 3", code)
	}
	left, _ := os.ReadDir(filepath.Join(dir, "machines"))
	for _, e := range left {
		t.Errorf("stale registry entry %q left after the container exited", e.Name())
	}
}

// TestPivotRootIgnoresImageSymlinks: the launcher created put_old as
// ROOTFS/mnt/old-root with MkdirAll before pivot_root. An image whose
// /mnt is an absolute symlink had that resolved against the HOST root,
// so root created directories wherever the image pointed.
func TestPivotRootIgnoresImageSymlinks(t *testing.T) {
	dir := t.TempDir()
	host := filepath.Join(dir, "host-target")
	if err := os.Mkdir(host, 0o755); err != nil {
		t.Fatal(err)
	}
	out, code := runInUserns(t, dir, `mkdir -p "$DIR/machines"
ln -s "$DIR/host-target" "$ROOTFS/mnt"
exec "$NSPAWN" --name t2 --boot "$ROOTFS" --machine-dir "$DIR/machines" --init /bin/sh -- -c 'exit 0'
`)
	t.Logf("exit=%d output:\n%s", code, out)
	if left, _ := os.ReadDir(host); len(left) != 0 {
		t.Errorf("the image's /mnt symlink made the launcher create %v in a host directory", left)
	}
	if code != 0 {
		t.Errorf("container did not start: exit %d", code)
	}
}

// TestLauncherFdsDoNotReachContainer: a descriptor the launcher
// inherited without close-on-exec went through the re-exec and the
// init exec into the container — a host file or directory reachable
// from inside, past pivot_root.
func TestLauncherFdsDoNotReachContainer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "host-secret"), []byte("HOST-SECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _ := runInUserns(t, dir, `mkdir -p "$DIR/machines"
exec 7<"$DIR/host-secret"
exec "$NSPAWN" --name t3 --boot "$ROOTFS" --machine-dir "$DIR/machines" --init /bin/sh -- -c 'cat <&7'
`)
	t.Logf("output:\n%s", out)
	if strings.Contains(out, "HOST-SECRET") {
		t.Error("the container's init read a host file through a descriptor inherited from the launcher")
	}
}

// TestContainerDevIsFresh: the container's /dev was the image's own,
// and the rootfs was bound without nodev, so a device node shipped in
// the image opened the host device it named. /dev is now a fresh
// tmpfs holding only the standard nodes, and the rootfs is nodev.
func TestContainerDevIsFresh(t *testing.T) {
	dir := t.TempDir()
	out, code := runInUserns(t, dir, `mkdir -p "$DIR/machines" "$ROOTFS/dev"
: > "$ROOTFS/dev/evil"; mount --bind /dev/null "$ROOTFS/dev/evil"
: > "$ROOTFS/evil"; mount --bind /dev/null "$ROOTFS/evil"
exec "$NSPAWN" --name t4 --boot "$ROOTFS" --machine-dir "$DIR/machines" --init /bin/sh -- -c '
echo DEV: $(cd /dev && echo *)
[ -e /dev/evil ] && echo IMAGE-DEV-VISIBLE
(echo x >/evil) 2>/dev/null && echo IMAGE-NODE-OPENS
echo x >/dev/null && head -c1 /dev/urandom >/dev/null && echo NODES-WORK
echo PTMX: $(readlink /dev/ptmx)'
`)
	t.Logf("exit=%d output:\n%s", code, out)
	for _, bad := range []string{"IMAGE-DEV-VISIBLE", "IMAGE-NODE-OPENS"} {
		if strings.Contains(out, bad) {
			t.Errorf("%s: a device node from the image is reachable in the container", bad)
		}
	}
	if !strings.Contains(out, "DEV: fd full null ptmx pts random shm stderr stdin stdout tty urandom zero\n") {
		t.Error("/dev does not hold exactly the standard nodes, links and mount points")
	}
	if !strings.Contains(out, "NODES-WORK") {
		t.Error("the container's /dev/null and /dev/urandom do not work")
	}
	if !strings.Contains(out, "PTMX: pts/ptmx") {
		t.Error("/dev/ptmx does not point at the container's devpts")
	}
}

// TestContainerProcProtected: the container's /proc was fully
// writable, /proc/sys included, where kernel.core_pattern runs a
// program as host root. /proc/sys and the other hardware and tunable
// directories are now read-only, and kcore, keys, kallsyms,
// sysrq-trigger and timer_list are covered, as under systemd-nspawn.
// With a private network the container's own net sysctls stay
// writable. The IPC namespace belongs to the test's user namespace,
// so kernel.msgmax is writable unless the mount is read-only.
func TestContainerProcProtected(t *testing.T) {
	dir := t.TempDir()
	out, code := runInUserns(t, dir, `mkdir -p "$DIR/machines"
exec "$NSPAWN" --name t5 --boot "$ROOTFS" --machine-dir "$DIR/machines" --private-network --init /bin/sh -- -c '
[ -r /proc/self/status ] && echo PROC-MOUNTED
(cat /proc/sys/kernel/msgmax >/proc/sys/kernel/msgmax) 2>/dev/null && echo SYS-WRITABLE
(echo 1 >/proc/sys/net/ipv4/ip_forward) 2>/dev/null && echo NET-WRITABLE
for f in kcore keys kallsyms sysrq-trigger timer_list; do [ -c /proc/$f ] || echo UNMASKED:$f; done
while read -r _ _ _ _ mp opts _; do case $mp in /proc/sys|/proc/bus|/proc/fs|/proc/irq|/sys) echo "MNT $mp ${opts%%,*}";; esac; done </proc/self/mountinfo'
`)
	t.Logf("exit=%d output:\n%s", code, out)
	if !strings.Contains(out, "PROC-MOUNTED") {
		t.Fatal("/proc is not mounted in the container")
	}
	if strings.Contains(out, "SYS-WRITABLE") {
		t.Error("/proc/sys is writable in the container")
	}
	if strings.Contains(out, "UNMASKED") {
		t.Error("a /proc entry systemd-nspawn makes inaccessible is reachable")
	}
	for _, mp := range []string{"/proc/sys", "/proc/bus", "/proc/fs", "/proc/irq", "/sys"} {
		if !strings.Contains(out, "MNT "+mp+" ro\n") && !strings.HasSuffix(out, "MNT "+mp+" ro") {
			t.Errorf("%s is not mounted read-only", mp)
		}
	}
	if !strings.Contains(out, "NET-WRITABLE") {
		t.Error("with --private-network the container's own net sysctls are not writable")
	}
}

// TestContainerCapabilities: the container's init ran with every
// capability: CAP_SYS_MODULE loads a kernel module, CAP_SYS_RAWIO
// reads /dev/mem, and so on. Its bounding set is now systemd-nspawn's
// default retained set, and no other capability reaches its permitted
// set, not even through an inheritable set the launcher was started
// with. With a private network CAP_NET_ADMIN stays, as under nspawn.
func TestContainerCapabilities(t *testing.T) {
	if _, err := exec.LookPath("setpriv"); err != nil {
		t.Skip("setpriv not available")
	}
	// systemd-nspawn's arg_caps_retain default.
	var nspawn uint64
	for _, c := range []uint{
		unix.CAP_AUDIT_CONTROL, unix.CAP_AUDIT_WRITE, unix.CAP_CHOWN,
		unix.CAP_DAC_OVERRIDE, unix.CAP_DAC_READ_SEARCH, unix.CAP_FOWNER,
		unix.CAP_FSETID, unix.CAP_IPC_OWNER, unix.CAP_KILL, unix.CAP_LEASE,
		unix.CAP_LINUX_IMMUTABLE, unix.CAP_MKNOD, unix.CAP_NET_BIND_SERVICE,
		unix.CAP_NET_BROADCAST, unix.CAP_NET_RAW, unix.CAP_SETFCAP,
		unix.CAP_SETGID, unix.CAP_SETPCAP, unix.CAP_SETUID, unix.CAP_SYS_ADMIN,
		unix.CAP_SYS_BOOT, unix.CAP_SYS_CHROOT, unix.CAP_SYS_NICE,
		unix.CAP_SYS_PTRACE, unix.CAP_SYS_RESOURCE, unix.CAP_SYS_TTY_CONFIG,
	} {
		nspawn |= 1 << c
	}
	dir := t.TempDir()
	out, code := runInUserns(t, dir, `mkdir -p "$DIR/machines"
setpriv --inh-caps +sys_module,+sys_time "$NSPAWN" --name t6 --boot "$ROOTFS" --machine-dir "$DIR/machines" --init /bin/sh -- -c 'grep ^Cap /proc/self/status'
"$NSPAWN" --name t7 --boot "$ROOTFS" --machine-dir "$DIR/machines" --private-network --init /bin/sh -- -c 'sed -n "s/^CapBnd:/PrivateNetBnd:/p" /proc/self/status'
`)
	t.Logf("exit=%d output:\n%s", code, out)
	caps := map[string]uint64{}
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) == 2 && strings.HasSuffix(f[0], ":") {
			v, err := strconv.ParseUint(f[1], 16, 64)
			if err != nil {
				t.Fatalf("%q: %v", l, err)
			}
			caps[strings.TrimSuffix(f[0], ":")] = v
		}
	}
	if len(caps) == 0 {
		t.Fatal("no capability sets read from the container")
	}
	if caps["CapBnd"] != nspawn {
		t.Errorf("CapBnd = %#x, want systemd-nspawn's %#x (extra %#x)", caps["CapBnd"], nspawn, caps["CapBnd"]&^nspawn)
	}
	for _, set := range []string{"CapPrm", "CapEff", "CapInh", "CapAmb"} {
		if extra := caps[set] &^ nspawn; extra != 0 {
			t.Errorf("%s holds %#x outside the retained set", set, extra)
		}
	}
	if want := nspawn | 1<<unix.CAP_NET_ADMIN; caps["PrivateNetBnd"] != want {
		t.Errorf("with --private-network CapBnd = %#x, want %#x", caps["PrivateNetBnd"], want)
	}
	if caps["CapEff"]&(1<<unix.CAP_SYS_ADMIN) == 0 {
		t.Error("the init lost CAP_SYS_ADMIN, which a full init in the container needs")
	}
}

// TestContainerCgroupNamespace: the container shared the host's cgroup
// namespace, so /proc/self/cgroup, and a cgroup2 mount made inside,
// showed the host's hierarchy and the container's place in it. With
// CLONE_NEWCGROUP the container's own cgroup is its root.
func TestContainerCgroupNamespace(t *testing.T) {
	host, err := os.ReadFile("/proc/self/cgroup")
	if err != nil || strings.TrimSpace(string(host)) == "0::/" {
		t.Skip("the test does not run below the cgroup root, nothing to tell apart")
	}
	dir := t.TempDir()
	out, code := runInUserns(t, dir, `mkdir -p "$DIR/machines"
exec "$NSPAWN" --name t8 --boot "$ROOTFS" --machine-dir "$DIR/machines" --init /bin/sh -- -c 'sed "s/^/CGROUP /" /proc/self/cgroup'
`)
	t.Logf("exit=%d output:\n%s", code, out)
	if !strings.Contains(out, "CGROUP 0::/\n") && !strings.HasSuffix(out, "CGROUP 0::/") {
		t.Error("the container sees the host's cgroup hierarchy, not its own cgroup as the root")
	}
}

// TestContainerSeccomp: the container's init ran with no seccomp
// filter. It now runs under one (see TestSeccompDenies for what it
// refuses).
func TestContainerSeccomp(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("pkg/seccomp has a system call table for amd64 only")
	}
	dir := t.TempDir()
	out, code := runInUserns(t, dir, `mkdir -p "$DIR/machines"
exec "$NSPAWN" --name t9 --boot "$ROOTFS" --machine-dir "$DIR/machines" --init /bin/sh -- -c 'sed -n "s/^Seccomp:[[:space:]]*/SECCOMP /p" /proc/self/status'
`)
	t.Logf("exit=%d output:\n%s", code, out)
	if !strings.Contains(out, "SECCOMP 2") {
		t.Error("the container's init runs without a seccomp filter")
	}
}
