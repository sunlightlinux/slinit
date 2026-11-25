package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// dropCredentials skipped setgroups when no --supp-gid was given, so a
// run-as service wrapped in the runner (any hardening directive does it)
// kept root's supplementary groups: `id` showed groups=65534(nobody),0(root)
// with no-new-privs and groups=65534(nobody) without. Before the fix
// this test saw groups [0] after the drop.
func TestDropCredentialsClearsSupplementaryGroups(t *testing.T) {
	if os.Geteuid() != 0 {
		if os.Getenv("SLINIT_TEST_USERNS") == "1" {
			t.Skip("not root inside the user namespace")
		}
		unshare, err := exec.LookPath("unshare")
		if err != nil {
			t.Skip("unshare not available")
		}
		if exec.Command(unshare, "--user", "--map-root-user", "--map-auto", "true").Run() != nil {
			t.Skip("unshare --user --map-root-user --map-auto not permitted here")
		}
		cmd := exec.Command(unshare, "--user", "--map-root-user", "--map-auto",
			os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v", "-test.count=1")
		cmd.Env = append(os.Environ(), "SLINIT_TEST_USERNS=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("in user namespace: %v\n%s", err, out)
		}
		if strings.Contains(string(out), "--- SKIP") {
			t.Skipf("skipped in user namespace:\n%s", out)
		}
		return
	}
	if !idMapped("/proc/self/uid_map", 65534) || !idMapped("/proc/self/gid_map", 65534) {
		t.Skip("uid/gid 65534 not mapped in this user namespace")
	}

	// The drop is per thread, as in the runner; this goroutine's thread
	// is discarded when the test returns still locked to it.
	runtime.LockOSThread()
	if err := unix.Setgroups([]int{0}); err != nil {
		t.Skipf("cannot set up a root supplementary group: %v", err)
	}
	if err := dropCredentials(65534, 65534, nil, nil); err != nil {
		t.Fatal(err)
	}
	groups, err := unix.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 0 {
		t.Errorf("supplementary groups after the drop = %v; want none", groups)
	}
}

func idMapped(mapFile string, id uint64) bool {
	b, err := os.ReadFile(mapFile)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		var in, out, n uint64
		if c, _ := fmt.Sscan(line, &in, &out, &n); c == 3 && id >= in && id < in+n {
			return true
		}
	}
	return false
}
