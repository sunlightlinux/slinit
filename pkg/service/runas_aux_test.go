package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// inRootUserNS reports whether the calling test may run its body: as
// root it can, otherwise the test re-runs itself under
// `unshare --user --map-root-user --map-auto` (root in a user namespace,
// with uid/gid 65534 mapped through /etc/subuid) and reports that run.
// It skips when no such namespace can be had.
func inRootUserNS(t *testing.T) bool {
	t.Helper()
	if os.Geteuid() == 0 {
		if !idMapped("/proc/self/uid_map", 65534) || !idMapped("/proc/self/gid_map", 65534) {
			t.Skip("uid/gid 65534 not mapped in this user namespace")
		}
		// Root in name only is possible: Ubuntu 24.04's
		// apparmor_restrict_unprivileged_userns lets the namespace be
		// created but grants no capabilities in it, so the switch to
		// the run-as user fails and every command under test silently
		// never runs. Probe the switch itself.
		probe := exec.Command("/bin/true")
		probe.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{Uid: 65534, Gid: 65534, Groups: []uint32{}},
		}
		if err := probe.Run(); err != nil {
			t.Skipf("cannot switch to uid 65534 here (%v): a user namespace without capabilities", err)
		}
		return true
	}
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
	return false
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

// A writable directory the run-as user can reach, for the commands
// under test to report `id` into.
func worldWritableDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for d := dir; d != os.TempDir() && d != "/"; d = filepath.Dir(d) {
		os.Chmod(d, 0o777)
	}
	return dir
}

func waitForFile(path string) string {
	for i := 0; i < 100; i++ {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return strings.TrimSpace(string(b))
		}
		time.Sleep(20 * time.Millisecond)
	}
	return ""
}

// stop-command ran as root for process and bgprocess services even with
// run-as set (scripted's ran as the user). Before the fix both reported
// uid 0 here.
func TestStopCommandHonoursRunAs(t *testing.T) {
	if !inRootUserNS(t) {
		return
	}
	dir := worldWritableDir(t)
	set, _ := newTestSet()

	pf := filepath.Join(dir, "process")
	p := NewProcessService(set, "p")
	p.SetRunAs(65534, 65534)
	p.SetStopCommand([]string{"/bin/sh", "-c", "id -u > " + pf})
	set.queueMu.Lock()
	p.execStopCommand()
	set.queueMu.Unlock()

	bf := filepath.Join(dir, "bgprocess")
	b := NewBGProcessService(set, "b")
	b.SetRunAs(65534, 65534)
	b.SetStopCommand([]string{"/bin/sh", "-c", "id -u > " + bf})
	b.execStopCommand()

	for kind, f := range map[string]string{"process": pf, "bgprocess": bf} {
		if got := waitForFile(f); got != "65534" {
			t.Errorf("%s stop-command ran as uid %q; want 65534 (run-as)", kind, got)
		}
	}
}

// finish-command, pre-stop-hook, control-command-* and
// ready-check-command ran as root too. Before the fix every one reported
// uid 0 (and the ready check failed).
func TestAuxCommandsHonourRunAs(t *testing.T) {
	if !inRootUserNS(t) {
		return
	}
	dir := worldWritableDir(t)
	set, _ := newTestSet()
	s := NewProcessService(set, "p")
	s.SetRunAs(65534, 65534)
	report := func(name string) []string {
		return []string{"/bin/sh", "-c", "id -u > " + filepath.Join(dir, name) + "; :"}
	}

	s.SetFinishCommand(report("finish"))
	s.execFinishCommand(process.ChildExit{})
	s.SetPreStopHook(report("pre-stop"))
	s.execPreStopHook()
	s.execControlCommand("HUP", report("control"))
	for _, name := range []string{"finish", "pre-stop", "control"} {
		if got := waitForFile(filepath.Join(dir, name)); got != "65534" {
			t.Errorf("%s ran as uid %q; want 65534 (run-as)", name, got)
		}
	}

	s.SetReadyCheckCommand([]string{"/bin/sh", "-c", `test "$(id -u)" = 65534`}, time.Second)
	if !s.runReadyCheckOnce() {
		t.Error("ready-check-command did not run as uid 65534 (run-as)")
	}
}
