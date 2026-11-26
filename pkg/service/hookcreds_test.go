package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// log-processor ran as root. It gets the service's run-as through
// LogRotatorConfig.PrepareProcessor.
func TestLogProcessorHonoursRunAs(t *testing.T) {
	if !inRootUserNS(t) {
		return
	}
	dir := worldWritableDir(t)
	set, _ := newTestSet()
	s := NewProcessService(set, "p")
	s.SetRunAs(65534, 65534)
	lr := &LogRotator{
		processor:   reportUID(dir, "log-processor"),
		prepareProc: s.prepareHookCmd,
	}
	lr.runProcessor("/dev/null")
	wantRunAsUID(t, dir, "log-processor")
}

// Without run-as nothing changes: no credential, the command stays
// slinit's own user.
func TestHookCmdNoRunAsKeepsSlinitCredentials(t *testing.T) {
	set, _ := newTestSet()
	p := NewProcessService(set, "p")
	sc := NewScriptedService(set, "sc")
	bg := NewBGProcessService(set, "bg")
	set.queueMu.Lock()
	defer set.queueMu.Unlock()
	for _, svc := range []hookCmdPreparer{p, sc, bg} {
		cmd := exec.Command("/bin/true")
		svc.prepareHookCmdLocked(cmd)
		if cmd.SysProcAttr != nil && cmd.SysProcAttr.Credential != nil {
			t.Errorf("%T without run-as got credential %+v", svc, cmd.SysProcAttr.Credential)
		}
	}
}

// The credential is added to what the caller set: cron's Setpgid stays.
func TestHookCmdKeepsSetpgid(t *testing.T) {
	set, _ := newTestSet()
	p := NewProcessService(set, "p")
	p.SetRunAs(65534, 65534)
	p.SetSupplementaryGroups([]uint32{10, 20})
	cmd := exec.Command("/bin/true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if !p.prepareHookCmd(context.Background(), cmd) {
		t.Fatal("prepareHookCmd refused with a live context")
	}
	if !cmd.SysProcAttr.Setpgid {
		t.Error("Setpgid lost")
	}
	c := cmd.SysProcAttr.Credential
	if c == nil || c.Uid != 65534 || c.Gid != 65534 || len(c.Groups) != 2 {
		t.Errorf("credential %+v; want 65534:65534 groups [10 20]", c)
	}
}

// Working directory and environment, as the finish-command gets them.
func TestCronCommandGetsWorkingDirAndEnv(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	set, _ := newTestSet()
	s := NewProcessService(set, "p")
	s.SetWorkingDir(dir)
	s.SetEnvVar("SLINIT_HOOK_PROBE", "cron-env")
	cr := NewCronRunner(s, []string{"/bin/sh", "-c",
		`echo "$(pwd) $SLINIT_HOOK_PROBE" > ` + out}, time.Second, 0, "", set.logger)
	if err := cr.executeCommand(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	if got, want := strings.TrimSpace(string(b)), dir+" cron-env"; got != want {
		t.Errorf("cron-command saw %q; want %q", got, want)
	}
}

// An exec-condition runs before the process that creates the managed
// directories: a working-dir that does not exist yet must not fail it.
func TestExecConditionMissingWorkingDir(t *testing.T) {
	set, _ := newTestSet()
	s := NewProcessService(set, "p")
	s.SetWorkingDir(filepath.Join(t.TempDir(), "not-yet"))
	s.SetPredicates([]Predicate{{Kind: PredExecCondition, Param: "true"}})
	set.queueMu.Lock()
	outcome, why := s.CheckPredicates()
	set.queueMu.Unlock()
	if outcome != PredOK {
		t.Errorf("exec-condition with a missing working-dir: %v %s", outcome, why)
	}
}

// BringDown stops the health checker with queueMu held and waits for
// its loop. A check waiting for that lock to read run-as must give up,
// or PID 1 deadlocks.
func TestHealthCheckStopUnderQueueMuWhileCheckWaits(t *testing.T) {
	set, _ := newTestSet()
	s := NewProcessService(set, "p")
	hc := NewHealthChecker(s, []string{"/bin/true"}, 50*time.Millisecond, 0, 0,
		nil, set.logger, nil)
	set.queueMu.Lock()
	hc.Start()
	time.Sleep(100 * time.Millisecond) // the first check is now waiting for the lock
	stopped := make(chan struct{})
	go func() {
		hc.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		set.queueMu.Unlock()
		t.Fatal("HealthChecker.Stop under queueMu deadlocked against a check waiting for the lock")
	}
	set.queueMu.Unlock()
	// The abandoned read lock is given back once the writer lets go.
	done := make(chan struct{})
	go func() {
		set.queueMu.Lock()
		set.queueMu.Unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("queueMu still held after the abandoned check")
	}
}

// A check firing while a reload changes run-as and working-dir (under
// queueMu, as the loader does) reads them under the same lock: run with
// -race.
func TestHookCmdConcurrentReload(t *testing.T) {
	set, _ := newTestSet()
	s := NewProcessService(set, "p")
	dir := t.TempDir()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			set.queueMu.Lock()
			s.SetRunAs(uint32(i%2)*65534, uint32(i%2)*65534)
			s.SetSupplementaryGroups([]uint32{uint32(i)})
			s.SetWorkingDir(dir)
			set.queueMu.Unlock()
		}
	}()
	for i := 0; i < 200; i++ {
		cmd := exec.Command("/bin/true")
		s.prepareHookCmd(context.Background(), cmd)
	}
	close(stop)
	wg.Wait()
}

// The log processor is wired to the service's credentials; the field
// stayed nil, so it ran as root whatever run-as said.
func TestRotatorConfigCarriesHookPreparer(t *testing.T) {
	set, _ := newTestSet()
	s := NewProcessService(set, "rotprep")
	if s.rotatorConfigFor("/tmp/x").PrepareProcessor == nil {
		t.Error("rotatorConfig leaves PrepareProcessor unset: log-processor runs as root")
	}
}

// A running service's health checks and cron jobs reuse the environment
// of the current start instead of running the env-generator per check.
func TestPeriodicHooksReuseStartEnv(t *testing.T) {
	set, _ := newTestSet()
	s := NewProcessService(set, "envreuse")
	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	s.SetEnvGenerator(writeScript(t, dir, "gen", "echo x >> "+count+"\necho GENV=1\n"))
	set.AddService(s)
	s.prepareStartEnv()
	for i := 0; i < 3; i++ {
		cmd := exec.Command("/bin/true")
		if !s.prepareHookCmd(context.Background(), cmd) {
			t.Fatal("prepareHookCmd refused")
		}
		found := false
		for _, kv := range cmd.Env {
			if kv == "GENV=1" {
				found = true
			}
		}
		if !found {
			t.Fatal("hook env lacks the generator's variable")
		}
	}
	b, _ := os.ReadFile(count)
	if n := strings.Count(string(b), "x"); n != 1 {
		t.Errorf("env-generator ran %d times for one start and 3 checks, want 1", n)
	}
}

// A hook's environment was the service's alone once the service had a
// variable of its own: cmd.Env set to it dropped PATH, HOME and the rest
// of slinit's environment, which the main process and a hook with no
// Env both inherit. Each hook path now gets slinit's environment
// (filtered as the main process's is) followed by the service's.
func TestHookCmdEnvInheritsSlinitEnv(t *testing.T) {
	t.Setenv("SLINIT_HOOKTEST_INHERITED", "yes")
	set, _ := newTestSet()
	p := NewProcessService(set, "p")
	sc := NewScriptedService(set, "sc")
	bg := NewBGProcessService(set, "bg")
	for _, r := range []*ServiceRecord{p.Record(), sc.Record(), bg.Record()} {
		r.SetEnvVar("SLINIT_HOOKTEST_OWN", "svc")
	}
	check := func(what string, env []string) {
		t.Helper()
		if !envHas(env, "SLINIT_HOOKTEST_INHERITED=yes") {
			t.Errorf("%s: slinit's own environment lost", what)
		}
		if !envHas(env, "SLINIT_HOOKTEST_OWN=svc") {
			t.Errorf("%s: the service's variable lost", what)
		}
	}

	set.queueMu.Lock()
	for _, svc := range []hookCmdPreparer{p, sc, bg} {
		cmd := exec.Command("/bin/true")
		svc.prepareHookCmdLocked(cmd)
		check(fmt.Sprintf("%T prepareHookCmdLocked", svc), cmd.Env)
	}
	set.queueMu.Unlock()

	cmd := exec.Command("/bin/true")
	if !p.prepareHookCmd(context.Background(), cmd) {
		t.Fatal("prepareHookCmd refused")
	}
	check("prepareHookCmd (cron, health, log-processor)", cmd.Env)
	check("finish/pre-stop/control/ready-check/pre-start", p.hookCmdEnv())

	// pass-environment filters it as it does the main process's.
	p.Record().SetPassEnvironment([]string{"SLINIT_HOOKTEST_OWN"}, true)
	env := p.hookCmdEnv()
	if envHas(env, "SLINIT_HOOKTEST_INHERITED=yes") {
		t.Error("pass-environment did not filter the inherited environment")
	}
	if !envHas(env, "SLINIT_HOOKTEST_OWN=svc") {
		t.Error("pass-environment dropped the allowed variable")
	}
}

// The finish-command itself sees an inherited variable.
func TestFinishCommandSeesInheritedEnv(t *testing.T) {
	t.Setenv("SLINIT_HOOKTEST_INHERITED", "yes")
	out := filepath.Join(t.TempDir(), "out")
	set, _ := newTestSet()
	s := NewProcessService(set, "p")
	s.SetEnvVar("SLINIT_HOOKTEST_OWN", "svc")
	s.SetFinishCommand([]string{"/bin/sh", "-c",
		`echo "$SLINIT_HOOKTEST_INHERITED $SLINIT_HOOKTEST_OWN" > ` + out + "; :"})
	s.execFinishCommand(process.ChildExit{})
	b, _ := os.ReadFile(out)
	if got := strings.TrimSpace(string(b)); got != "yes svc" {
		t.Errorf("finish-command saw %q; want %q", got, "yes svc")
	}
}

// A cron run whose window passed while it waited for queueMu (a start
// or stop holding it longer than the interval) returned ctx.Err(): the
// tick was logged as a failure, and with cron-on-error=stop the job
// ended for good though the command never ran. The tick is skipped now.
func TestCronLockWaitPastIntervalIsNotAFailure(t *testing.T) {
	out := filepath.Join(t.TempDir(), "ran")
	set, _ := newTestSet()
	s := NewProcessService(set, "p")
	cr := NewCronRunner(s, []string{"/bin/sh", "-c", "touch " + out},
		100*time.Millisecond, 0, "stop", set.logger)
	set.queueMu.Lock()
	time.AfterFunc(300*time.Millisecond, set.queueMu.Unlock)
	if err := cr.executeCommand(); err != nil {
		t.Errorf("a run that never got the lock reported %v", err)
	}
	set.queueMu.Lock() // the holder is done
	set.queueMu.Unlock()
	if !cr.runOnce() {
		t.Error("cron-on-error=stop ended the job after a skipped tick")
	}
	if _, err := os.Stat(out); err != nil {
		t.Error("the next tick, with the lock free, did not run the command")
	}
}
