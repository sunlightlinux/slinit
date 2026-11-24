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
