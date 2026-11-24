package process

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// reexecAsRootWithMounts re-runs the calling test as root in a new user
// and mount namespace, with a private tmpfs on /run, and reports that
// run. It returns true in the re-run, where the test body belongs, and
// skips when no such namespace can be had.
func reexecAsRootWithMounts(t *testing.T) bool {
	t.Helper()
	if os.Getenv("SLINIT_TEST_MOUNTNS") == "1" {
		if err := unix.Mount("tmpfs", "/run", "tmpfs", 0, ""); err != nil {
			t.Skipf("mount tmpfs on /run: %v", err)
		}
		return true
	}
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skip("unshare not available")
	}
	if exec.Command(unshare, "--user", "--map-root-user", "--mount", "true").Run() != nil {
		t.Skip("unshare --user --map-root-user --mount not permitted here")
	}
	cmd := exec.Command(unshare, "--user", "--map-root-user", "--mount", "--propagation", "private",
		os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), "SLINIT_TEST_MOUNTNS=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("in mount namespace: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "--- SKIP") {
		t.Skipf("skipped in mount namespace:\n%s", out)
	}
	return false
}

func credentialMounts(t *testing.T, dir string) int {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), " "+dir+" ")
}

// A stop-command gets the same Credentials as the main command, so a
// second SetupCredentials ran while the first tmpfs was still mounted
// and stacked another on top. CleanupCredentials unmounted one,
// RemoveAll then failed with EROFS on the other, and the secret stayed
// readable after the service stopped. Before the fix: 2 mounts after
// start + stop-command, cleanup "read-only file system", and the
// credential still readable.
func TestCredentialsNotStackedOrLeftBehind(t *testing.T) {
	if !reexecAsRootWithMounts(t) {
		return
	}
	src := []CredentialSource{{Name: "token", Value: "s3cret"}}
	dir := CredentialsDir("svc")
	for i := 0; i < 2; i++ { // main command, then stop-command
		if _, err := SetupCredentials("svc", src, 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	if n := credentialMounts(t, dir); n != 1 {
		t.Errorf("%d tmpfs mounts at %s after two setups; want 1", n, dir)
	}
	if err := CleanupCredentials("svc"); err != nil {
		t.Errorf("CleanupCredentials: %v", err)
	}
	if n := credentialMounts(t, dir); n != 0 {
		t.Errorf("%d tmpfs mounts left at %s after cleanup; want 0", n, dir)
	}
	if b, err := os.ReadFile(dir + "/token"); err == nil {
		t.Errorf("credential still readable after cleanup: %q", b)
	}
}

// Cleanup must also clear mounts that were stacked by other means (a
// lingering mount from before a re-exec), not just the top one.
func TestCleanupCredentialsUnmountsEveryLayer(t *testing.T) {
	if !reexecAsRootWithMounts(t) {
		return
	}
	dir := CredentialsDir("svc")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := unix.Mount("tmpfs", dir, "tmpfs", 0, "size=1M"); err != nil {
			t.Fatal(err)
		}
	}
	if err := CleanupCredentials("svc"); err != nil {
		t.Errorf("CleanupCredentials: %v", err)
	}
	if n := credentialMounts(t, dir); n != 0 {
		t.Errorf("%d tmpfs mounts left at %s after cleanup; want 0", n, dir)
	}
}
