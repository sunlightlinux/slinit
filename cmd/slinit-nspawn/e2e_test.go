package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
