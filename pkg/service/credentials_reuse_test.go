package service

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/process"
	"golang.org/x/sys/unix"
)

// inMountNS re-runs the calling test as root in a new user and mount
// namespace, with a private tmpfs on /run, and reports that run. It
// returns true in the re-run, where the test body belongs, and skips
// when no such namespace can be had.
func inMountNS(t *testing.T) bool {
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

// A stop-command set up its own credentials tmpfs over the one the
// running daemon was using, and when it then failed to start (here, a
// missing binary) the failed start's cleanup unmounted and removed the
// directory, so the daemon, still running, lost its credentials. Before
// the fix: "credential gone after a failed stop-command" for both types.
func TestFailedStopCommandKeepsDaemonCredentials(t *testing.T) {
	if !inMountNS(t) {
		return
	}
	set, _ := newTestSet()
	creds := []process.CredentialSource{{Name: "token", Value: "s3cret"}}

	p := NewProcessService(set, "p")
	b := NewBGProcessService(set, "b")
	for _, rec := range []*ServiceRecord{p.Record(), b.Record()} {
		rec.SetCredentials(creds)
		// What the main command's start set up, still in use by it.
		if _, err := process.SetupCredentials(rec.serviceName, creds, 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	p.SetStopCommand([]string{"/nonexistent/stop-command"})
	b.SetStopCommand([]string{"/nonexistent/stop-command"})

	set.queueMu.Lock()
	pStarted := p.execStopCommand()
	set.queueMu.Unlock()
	bStarted := b.execStopCommand()
	if pStarted || bStarted {
		t.Fatal("a missing stop-command binary started")
	}

	for _, name := range []string{"p", "b"} {
		dir := process.CredentialsDir(name)
		if got, err := os.ReadFile(dir + "/token"); err != nil || string(got) != "s3cret" {
			t.Errorf("%s: credential gone after a failed stop-command: %q, %v", name, got, err)
		}
		mi, _ := os.ReadFile("/proc/self/mountinfo")
		if n := strings.Count(string(mi), " "+dir+" "); n != 1 {
			t.Errorf("%s: %d mounts at %s; want the main command's one", name, n, dir)
		}
	}
}
