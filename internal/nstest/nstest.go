// Package nstest re-runs a test as root of an unprivileged user namespace,
// so tests that need to chown, chroot or mount run on a developer's
// machine instead of skipping there. Only tests import it, so it is not
// linked into any shipped binary.
package nstest

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const envMarker = "SLINIT_TEST_USERNS"

// Opts says what the namespace needs beyond root mapped to the caller.
type Opts struct {
	// IDs maps the caller's subordinate uids and gids too (unshare
	// --map-auto, through newuidmap and /etc/subuid), for tests that
	// chown to ids other than root.
	IDs bool
	// Mount gives the test a private mount namespace.
	Mount bool
}

// AsRoot returns true when the test body should run: the test is already
// root, or it is the re-run inside the namespace. Otherwise it re-runs
// the test there, reports the re-run's outcome as its own (failure, or a
// skip with the re-run's reason) and returns false; the caller returns.
// It skips when no such namespace can be had, saying why.
func AsRoot(t *testing.T, o Opts) bool {
	t.Helper()
	if os.Getenv(envMarker) == "1" {
		if os.Geteuid() != 0 {
			t.Skip("not root inside the user namespace")
		}
		probeCaps(t, o)
		return true
	}
	if os.Geteuid() == 0 {
		return true
	}
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skip("needs root, and unshare is not available to make a user namespace")
	}
	args := []string{"--user", "--map-root-user"}
	if o.IDs {
		args = append(args, "--map-auto")
	}
	if o.Mount {
		args = append(args, "--mount", "--propagation", "private")
	}
	if out, err := exec.Command(unshare, append(args, "true")...).CombinedOutput(); err != nil {
		why := ""
		if o.IDs {
			why = " (--map-auto needs newuidmap/newgidmap and the caller's range in /etc/subuid and /etc/subgid)"
		}
		t.Skipf("needs root, and unshare %s is not permitted here%s: %v %s",
			strings.Join(args, " "), why, err, strings.TrimSpace(string(out)))
	}
	cmd := exec.Command(unshare, append(args, os.Args[0],
		"-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.v", "-test.count=1")...)
	cmd.Env = append(os.Environ(), envMarker+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("in a user namespace: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "--- SKIP") {
		t.Skipf("skipped in a user namespace:\n%s", out)
	}
	if !strings.Contains(string(out), "--- PASS: "+t.Name()) {
		t.Fatalf("the re-run in a user namespace did not run %s:\n%s", t.Name(), out)
	}
	return false
}

// Inside reports whether this is the re-run inside the namespace, for a
// test that sets up its namespace (mounts) only there, never on a host it
// really is root of.
func Inside() bool { return os.Getenv(envMarker) == "1" }

// probeCaps skips when the namespace came without the capabilities that
// make it useful: an AppArmor-restricted userns can be created but leaves
// its root unable to chown.
func probeCaps(t *testing.T, o Opts) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	id := 0
	if o.IDs {
		id = 1
	}
	if err := os.Chown(p, id, id); err != nil {
		t.Skipf("user namespace root cannot chown (restricted user namespaces?): %v", err)
	}
	os.Remove(p)
}
