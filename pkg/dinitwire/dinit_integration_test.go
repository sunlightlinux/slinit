package dinitwire

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// dinitBinary finds a dinit to test against: $SLINIT_TEST_DINIT, else dinit
// on PATH. The layouts in this package were read out of dinit's source, so
// a test built from the same reading would only check the reading against
// itself; these run against the daemon.
func dinitBinary(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("SLINIT_TEST_DINIT"); p != "" {
		return p
	}
	if p, err := exec.LookPath("dinit"); err == nil {
		return p
	}
	t.Skip("no dinit binary: set SLINIT_TEST_DINIT (make in a dinit checkout builds src/dinit, no install needed) or put dinit on PATH")
	return ""
}

// startDinit runs a user-mode dinit over a fresh service directory and
// returns a connection to it and that directory.
func startDinit(t *testing.T) (*Conn, string) {
	t.Helper()
	bin := dinitBinary(t)

	// A unix socket path is limited to 108 bytes, which t.TempDir() can
	// exceed; keep it short.
	dir, err := os.MkdirTemp("", "dw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	if len(sock) > 100 {
		t.Skipf("temp dir too long for a unix socket path: %s", sock)
	}
	svc := filepath.Join(dir, "svc")
	if err := os.Mkdir(svc, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(svc, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("boot", "type = internal\nwaits-for: runner\n")
	write("runner", "type = process\ncommand = /bin/sleep 600\n")
	write("leaf", "type = process\ncommand = /bin/sleep 600\ndepends-on: runner\n")
	write("trig", "type = triggered\n")
	write("logged", "type = process\ncommand = /bin/sh -c \"echo from-the-buffer; exec sleep 600\"\nlog-type = buffer\n")
	write("oneshot", "type = scripted\ncommand = /bin/true\n")

	cmd := exec.Command(bin, "-u", "-d", svc, "-p", sock, "-l", filepath.Join(dir, "log"))
	if err := cmd.Start(); err != nil {
		t.Fatalf("start dinit: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})

	var nc net.Conn
	deadline := time.Now().Add(5 * time.Second)
	for {
		nc, err = net.Dial("unix", sock)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dinit socket never came up: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { nc.Close() })
	_ = nc.SetDeadline(time.Now().Add(20 * time.Second))
	return New(nc), svc
}

func mustLoad(t *testing.T, c *Conn, name string) ServiceRecord {
	t.Helper()
	code, rec, err := c.FindOrLoad(CmdLoadService, name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	if code != RplyServiceRecord {
		t.Fatalf("load %s: reply %d, want service record", name, code)
	}
	return rec
}

// waitState polls a service's status until it reaches want.
func waitState(t *testing.T, c *Conn, h uint32, want uint8) Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, st, err := c.Status(CmdServiceStatus5, h)
		if err != nil || code != RplyServiceStatus {
			t.Fatalf("status: code %d err %v", code, err)
		}
		if st.State == want {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("service never reached state %d (now %d)", want, st.State)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

const (
	stStopped   = 0
	stStarted   = 2
	rplyACK     = 50
	rplyNAK     = 51
	rplyNoSvc   = 60
	rplyAlready = 61
)

func TestDinitVersion(t *testing.T) {
	c, _ := startDinit(t)
	min, actual, err := c.QueryVersion()
	if err != nil {
		t.Fatal(err)
	}
	if min != 1 || actual < 7 {
		t.Errorf("version: min %d actual %d, want min 1 and actual >= 7", min, actual)
	}
}

func TestDinitFindLoadAndStatus(t *testing.T) {
	c, _ := startDinit(t)

	if code, _, err := c.FindOrLoad(CmdFindService, "no-such-service"); err != nil || code != rplyNoSvc {
		t.Errorf("find of a missing service: code %d err %v, want NOSERVICE", code, err)
	}

	rec := mustLoad(t, c, "runner")
	st := waitState(t, c, rec.Handle, stStarted)
	if !st.HasPID || st.PID <= 0 {
		t.Errorf("a started process service reports no pid: %+v", st)
	}
	if st.Flags&FlagMarkedActive != 0 {
		t.Errorf("runner is started only as a dependency, yet is marked active: flags %#x", st.Flags)
	}

	// All three status versions agree on the head and the pid.
	for _, cmd := range []uint8{CmdServiceStatus, CmdServiceStatus5, CmdServiceStatus6} {
		code, got, err := c.Status(cmd, rec.Handle)
		if err != nil || code != RplyServiceStatus {
			t.Fatalf("status cmd %d: code %d err %v", cmd, code, err)
		}
		if got.State != stStarted || got.PID != st.PID {
			t.Errorf("status cmd %d: state %d pid %d, want started pid %d", cmd, got.State, got.PID, st.PID)
		}
		if cmd == CmdServiceStatus6 && got.ModSec == 0 {
			t.Errorf("status6 carries no description mod time: %+v", got)
		}
	}

	code, name, err := c.QueryServiceName(rec.Handle)
	if err != nil || code != RplyServiceName || name != "runner" {
		t.Errorf("query name: code %d name %q err %v", code, name, err)
	}
}

func TestDinitList(t *testing.T) {
	c, _ := startDinit(t)
	rec := mustLoad(t, c, "runner")
	waitState(t, c, rec.Handle, stStarted)

	list, err := c.List()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Status{}
	for _, e := range list {
		got[e.Name] = e.Status
	}
	if _, ok := got["boot"]; !ok {
		t.Errorf("boot missing from the list: %v", got)
	}
	r, ok := got["runner"]
	if !ok || r.State != stStarted || !r.HasPID {
		t.Errorf("runner in the list: %+v (present %v), want started with a pid", r, ok)
	}
}

func TestDinitStartStopAndEvents(t *testing.T) {
	c, _ := startDinit(t)
	leaf := mustLoad(t, c, "leaf")

	rep, err := c.StartStop(CmdStartService, StartStopPreACK, leaf.Handle)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.PreACK || rep.Code != rplyACK {
		t.Fatalf("start: %+v, want a pre-ack then ACK", rep)
	}
	// Holding the handle, the start arrives as an event.
	deadline := time.Now().Add(5 * time.Second)
	for {
		ev, err := c.NextEvent()
		if err != nil {
			t.Fatalf("waiting for the started event: %v", err)
		}
		if ev.Handle == leaf.Handle && ev.Event == 0 /* STARTED */ {
			if ev.Status.State != stStarted {
				t.Errorf("started event carries state %d", ev.Status.State)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no started event for leaf")
		}
	}

	if rep, err = c.StartStop(CmdStartService, 0, leaf.Handle); err != nil || rep.Code != rplyAlready {
		t.Errorf("start of a started service: %+v err %v, want ALREADYSS", rep, err)
	}

	// A gentle stop of runner is refused with its dependents: boot holds it
	// only by waits-for, leaf by depends-on.
	runner := mustLoad(t, c, "runner")
	rep, err = c.StartStop(CmdStopService, StartStopGentle, runner.Handle)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Code != RplyDependents || len(rep.Dependents) != 1 {
		t.Fatalf("gentle stop of runner: %+v, want one dependent", rep)
	}
	if code, name, _ := c.QueryServiceName(rep.Dependents[0]); code != RplyServiceName || name != "leaf" {
		t.Errorf("the dependent is %q (code %d), want leaf", name, code)
	}

	if rep, err = c.StartStop(CmdStopService, 0, leaf.Handle); err != nil || rep.Code != rplyACK {
		t.Fatalf("stop leaf: %+v err %v", rep, err)
	}
	st := waitState(t, c, leaf.Handle, stStopped)
	if st.HasPID {
		t.Errorf("a stopped service still reports a pid: %+v", st)
	}
}

func TestDinitTriggerSignalCatlog(t *testing.T) {
	c, _ := startDinit(t)

	trig := mustLoad(t, c, "trig")
	if code, err := c.SetTrigger(trig.Handle, true); err != nil || code != rplyACK {
		t.Errorf("trigger: code %d err %v", code, err)
	}
	if code, err := c.SetTrigger(trig.Handle, false); err != nil || code != rplyACK {
		t.Errorf("untrigger: code %d err %v", code, err)
	}

	logged := mustLoad(t, c, "logged")
	if rep, err := c.StartStop(CmdStartService, 0, logged.Handle); err != nil || rep.Code != rplyACK {
		t.Fatalf("start logged: %+v err %v", rep, err)
	}
	waitState(t, c, logged.Handle, stStarted)

	deadline := time.Now().Add(5 * time.Second)
	for {
		code, data, err := c.CatLog(0, logged.Handle)
		if err != nil || code != RplyServiceLog {
			t.Fatalf("catlog: code %d err %v", code, err)
		}
		if strings.Contains(string(data), "from-the-buffer") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("catlog never showed the line; got %q", data)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if code, err := c.Signal(int32(syscall.SIGHUP), logged.Handle); err != nil || code != rplyACK {
		t.Errorf("signal a running service: code %d err %v, want ACK", code, err)
	}
	if code, err := c.Signal(int32(syscall.SIGHUP), trig.Handle); err != nil || code != 74 /* SIGNAL_NOPID */ {
		t.Errorf("signal a service with no process: code %d err %v, want SIGNAL_NOPID", code, err)
	}
	// A service with no buffer refuses catlog.
	if code, _, err := c.CatLog(0, trig.Handle); err != nil || code != rplyNAK {
		t.Errorf("catlog of an unbuffered service: code %d err %v, want NAK", code, err)
	}
}

func TestDinitEnv(t *testing.T) {
	c, _ := startDinit(t)
	if code, err := c.SetEnv("DINITWIRE_PROBE=one two"); err != nil || code != rplyACK {
		t.Fatalf("setenv: code %d err %v", code, err)
	}
	has := func(want string) bool {
		code, env, err := c.GetAllEnv()
		if err != nil || code != RplyAllEnv {
			t.Fatalf("getallenv: code %d err %v", code, err)
		}
		for _, e := range env {
			if e == want {
				return true
			}
		}
		return false
	}
	if !has("DINITWIRE_PROBE=one two") {
		t.Error("the variable set is not in the daemon's environment")
	}
	if code, err := c.SetEnv("DINITWIRE_PROBE"); err != nil || code != rplyACK {
		t.Fatalf("unsetenv: code %d err %v", code, err)
	}
	if has("DINITWIRE_PROBE=one two") {
		t.Error("the variable is still there after being unset")
	}
}

func TestDinitDepsAndDirs(t *testing.T) {
	c, svcDir := startDinit(t)
	boot := mustLoad(t, c, "boot")
	one := mustLoad(t, c, "oneshot")
	const waitsFor = 2

	if rep, err := c.Dep(CmdAddDep, waitsFor, boot.Handle, one.Handle); err != nil || rep.Code != rplyACK {
		t.Fatalf("add-dep: %+v err %v", rep, err)
	}
	if rep, err := c.Dep(CmdRmDep, waitsFor, boot.Handle, one.Handle); err != nil || rep.Code != rplyACK {
		t.Fatalf("rm-dep: %+v err %v", rep, err)
	}
	// Removing it again: the plain command says NAK, the V7 one answers
	// with a status and "not removed".
	if rep, err := c.Dep(CmdRmDep, waitsFor, boot.Handle, one.Handle); err != nil || rep.Code != rplyNAK {
		t.Errorf("rm-dep of an absent dependency: %+v err %v, want NAK", rep, err)
	}
	rep, err := c.Dep(CmdRmDepV7, waitsFor, boot.Handle, one.Handle)
	if err != nil || rep.Code != RplyServiceStatus || rep.Flag {
		t.Errorf("rm-dep V7 of an absent dependency: %+v err %v, want a status with the flag clear", rep, err)
	}
	// Enable adds the dependency and starts the target.
	rep, err = c.Dep(CmdEnableServiceV7, waitsFor, boot.Handle, one.Handle)
	if err != nil || rep.Code != RplyServiceStatus || rep.Flag {
		t.Errorf("enable V7: %+v err %v, want a status and \"did not exist\"", rep, err)
	}
	waitState(t, c, one.Handle, stStarted)

	code, dir, err := c.QueryServiceDscDir(boot.Handle)
	if err != nil || code != RplySvcDscDir || dir != svcDir {
		t.Errorf("description dir: code %d dir %q err %v, want %q", code, dir, err, svcDir)
	}
	code, lm, err := c.QueryLoadMech()
	if err != nil || code != RplyLoaderMech {
		t.Fatalf("load mech: code %d err %v", code, err)
	}
	if len(lm.Dirs) != 1 || lm.Dirs[0] != svcDir || lm.Cwd == "" {
		t.Errorf("load mech: %+v, want one dir %q and a cwd", lm, svcDir)
	}
}

func TestDinitHandleCommands(t *testing.T) {
	c, _ := startDinit(t)
	one := mustLoad(t, c, "oneshot")

	if code, err := c.HandleCmd(CmdUnpinService, one.Handle); err != nil || code != rplyACK {
		t.Errorf("unpin: code %d err %v", code, err)
	}
	if code, err := c.HandleCmd(CmdReloadService, one.Handle); err != nil || code != rplyACK {
		t.Errorf("reload of a stopped service: code %d err %v", code, err)
	}
	// Reload dropped the old record; the handle is for the new one.
	one = mustLoad(t, c, "oneshot")
	if code, err := c.HandleCmd(CmdUnloadService, one.Handle); err != nil || code != rplyACK {
		t.Errorf("unload of a stopped, unreferenced service: code %d err %v", code, err)
	}
	if code, _, err := c.FindOrLoad(CmdFindService, "oneshot"); err != nil || code != rplyNoSvc {
		t.Errorf("after unload, find: code %d err %v, want NOSERVICE", code, err)
	}

	trig := mustLoad(t, c, "trig")
	if code, err := c.HandleCmd(CmdCloseHandle, trig.Handle); err != nil || code != rplyACK {
		t.Errorf("close handle: code %d err %v", code, err)
	}
	if code, err := c.ListenEnv(); err != nil || code != rplyACK {
		t.Errorf("listenenv: code %d err %v", code, err)
	}
	if code, err := c.SetEnv("DINITWIRE_EVT=1"); err != nil || code != rplyACK {
		t.Fatalf("setenv: code %d err %v", code, err)
	}
	ev, err := c.NextEvent()
	if err != nil || ev.Type != InfoEnvEvent || ev.Var != "DINITWIRE_EVT=1" {
		t.Errorf("environment event: %+v err %v", ev, err)
	}
}
