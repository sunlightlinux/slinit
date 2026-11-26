package service

import (
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// pre-start-command and post-start-command ran as root whatever run-as
// said; systemd runs ExecStartPre= and ExecStartPost= as User=. Each
// reported uid 0 here before. With the "+" prefix (PrivilegedHooks)
// they run as root, as systemd's ExecStartPre=+ does.
func TestStartHooksHonourRunAs(t *testing.T) {
	if !inRootUserNS(t) {
		return
	}
	for _, tc := range []struct {
		name string
		priv bool
		want string
	}{{"run-as", false, "65534"}, {"plus", true, "0"}} {
		dir := worldWritableDir(t)
		set, _ := newTestSet()
		s := NewProcessService(set, "p-"+tc.name)
		s.SetRunAs(65534, 65534)
		s.Record().SetPrivilegedHooks(PrivilegedHooks{PreStart: tc.priv, PostStart: tc.priv})
		s.SetPreStartCommand(reportUIDOK(dir, "pre-start"))
		s.SetPostStartCommand(reportUIDOK(dir, "post-start"))
		s.SetCommand([]string{"/bin/sleep", "30"})
		set.AddService(s)
		set.StartService(s)
		for _, n := range []string{"pre-start", "post-start"} {
			if got := waitForFile(filepath.Join(dir, n)); got != tc.want {
				t.Errorf("%s: %s-command ran as uid %q; want %s", tc.name, n, got, tc.want)
			}
		}
		set.StopService(s)
		waitState(t, s, StateStopped, 5*time.Second)
	}
}

// The same on the on-demand path, where the pre-start-command runs
// when the first client connects.
func TestOnDemandPreStartHonoursRunAs(t *testing.T) {
	if !inRootUserNS(t) {
		return
	}
	for _, tc := range []struct {
		name string
		priv bool
		want string
	}{{"run-as", false, "65534"}, {"plus", true, "0"}} {
		dir := worldWritableDir(t)
		sock := filepath.Join(dir, "od.sock")
		set, _ := newTestSet()
		svc := newOnDemand(t, set, "od-"+tc.name, sock, []string{"/bin/sleep", "30"})
		svc.SetRunAs(65534, 65534)
		svc.Record().SetPrivilegedHooks(PrivilegedHooks{PreStart: tc.priv})
		svc.SetPreStartCommand(reportUIDOK(dir, "pre-start"))
		set.StartService(svc)
		if got := waitState(t, svc, StateStarted, 2*time.Second); got != StateStarted {
			t.Fatalf("%s: state = %v, want STARTED", tc.name, got)
		}
		conn, err := net.DialTimeout("unix", sock, 2*time.Second)
		if err != nil {
			t.Fatalf("%s: dial: %v", tc.name, err)
		}
		if got := waitForFile(filepath.Join(dir, "pre-start")); got != tc.want {
			t.Errorf("%s: on-demand pre-start-command ran as uid %q; want %s", tc.name, got, tc.want)
		}
		conn.Close()
		stopAndWait(t, set, svc)
	}
}

// "+" on the hooks that already ran as run-as: finish-command,
// pre-stop-hook, control-command-*, stop-command (process, bgprocess,
// scripted) and exec-condition run as root with it.
func TestPrivilegedHooksRunAsRoot(t *testing.T) {
	if !inRootUserNS(t) {
		return
	}
	dir := worldWritableDir(t)
	set, _ := newTestSet()
	s := NewProcessService(set, "p")
	s.SetRunAs(65534, 65534)
	s.Record().SetPrivilegedHooks(PrivilegedHooks{Finish: true, PreStop: true,
		Stop: true, Control: map[string]bool{"HUP": true}})
	s.SetFinishCommand(reportUIDOK(dir, "finish"))
	s.execFinishCommand(process.ChildExit{})
	s.SetPreStopHook(reportUIDOK(dir, "pre-stop"))
	s.execPreStopHook()
	s.execControlCommand("HUP", reportUIDOK(dir, "control-HUP"))
	// Without the flag for its signal, a control command keeps run-as.
	s.execControlCommand("USR1", reportUIDOK(dir, "control-USR1"))
	s.SetStopCommand(reportUIDOK(dir, "stop-process"))
	set.queueMu.Lock()
	s.execStopCommand()
	set.queueMu.Unlock()

	b := NewBGProcessService(set, "b")
	b.SetRunAs(65534, 65534)
	b.Record().SetPrivilegedHooks(PrivilegedHooks{Stop: true})
	b.SetStopCommand(reportUIDOK(dir, "stop-bgprocess"))
	b.execStopCommand()

	sc := NewScriptedService(set, "sc")
	sc.SetRunAs(65534, 65534)
	sc.Record().SetPrivilegedHooks(PrivilegedHooks{Stop: true})
	sc.SetStopCommand(reportUIDOK(dir, "stop-scripted"))
	set.AddService(sc)
	set.queueMu.Lock()
	sc.BringDown()
	set.queueMu.Unlock()

	c := NewProcessService(set, "c")
	c.SetRunAs(65534, 65534)
	c.SetPredicates([]Predicate{
		{Kind: PredExecCondition, Param: "id -u > " + filepath.Join(dir, "cond-plus"), Privileged: true},
		{Kind: PredExecCondition, Param: "id -u > " + filepath.Join(dir, "cond")},
	})
	set.queueMu.Lock()
	if outcome, why := c.Record().CheckPredicates(); outcome != PredOK {
		t.Errorf("exec-condition failed: %s", why)
	}
	set.queueMu.Unlock()

	for n, want := range map[string]string{
		"finish": "0", "pre-stop": "0", "control-HUP": "0", "control-USR1": "65534",
		"stop-process": "0", "stop-bgprocess": "0", "stop-scripted": "0",
		"cond-plus": "0", "cond": "65534",
	} {
		if got := waitForFile(filepath.Join(dir, n)); got != want {
			t.Errorf("%s ran as uid %q; want %s", n, got, want)
		}
	}
}

func reportUIDOK(dir, name string) []string {
	return []string{"/bin/sh", "-c", "id -u > " + filepath.Join(dir, name) + "; :"}
}

// FullyPrivileged leaves the cgroup and resource settings, and takes the
// identity and the sandbox away.
func TestFullyPrivilegedParams(t *testing.T) {
	p := process.ExecParams{
		RunAsUID: 65534, RunAsGID: 65534, SupplementaryGIDs: []uint32{1},
		NoNewPrivs: true, AmbientCaps: []uintptr{1}, BoundingCaps: []uintptr{1}, Securebits: 1,
		ProtectSystem: "strict", PrivateTmp: true, SeccompFilter: []string{"@system-service"},
		RestrictNamespaces: true, Cloneflags: 1, AppArmorProfile: "x",
		CgroupPath: "/sys/fs/cgroup/x", Rlimits: []process.Rlimit{{}}, WorkingDir: "/w",
	}
	p.FullyPrivileged()
	if p.RunAsUID != 0 || p.RunAsGID != 0 || p.SupplementaryGIDs != nil || p.NoNewPrivs ||
		p.AmbientCaps != nil || p.BoundingCaps != nil || p.Securebits != 0 ||
		p.ProtectSystem != "" || p.PrivateTmp || p.SeccompFilter != nil ||
		p.RestrictNamespaces || p.Cloneflags != 0 || p.AppArmorProfile != "" {
		t.Errorf("privilege limits left: %+v", p)
	}
	if p.CgroupPath == "" || len(p.Rlimits) != 1 || p.WorkingDir != "/w" {
		t.Errorf("cgroup/resource settings dropped: %+v", p)
	}
}
