package eventloop

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// After Run returns, PID 1 still has its final shutdown sequence (and a
// soft reboot its hooks and sweep, a ShutdownRemain hold for ever) in
// front of it. Run used to signal.Stop its channel on the way out,
// handing SIGINT/SIGTERM/SIGHUP/SIGQUIT back to the Go runtime, whose
// action is to exit: as PID 1 that is a kernel panic. The test re-runs
// itself as PID 1 of a new pid namespace, stops a loop, and then sends
// itself SIGINT and SIGQUIT.
func TestPID1SurvivesSignalsAfterRun(t *testing.T) {
	if os.Getenv("SLINIT_TEST_PID1_CHILD") == "1" {
		pid1Child()
		return
	}
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not available")
	}
	cmd := exec.Command("unshare", "-Urpf", "--mount-proc",
		os.Args[0], "-test.run=^TestPID1SurvivesSignalsAfterRun$", "-test.v")
	cmd.Env = append(os.Environ(), "SLINIT_TEST_PID1_CHILD=1")
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 && len(out) > 0 && !strings.Contains(string(out), "PID1-CHILD") {
		t.Skipf("cannot create a pid namespace here: %s", out)
	}
	if err != nil || !strings.Contains(string(out), "PID1-SURVIVED") {
		t.Fatalf("PID 1 died after Run returned: err=%v\n%s", err, out)
	}
}

func pid1Child() {
	os.Stdout.WriteString("PID1-CHILD pid=" + strconv.Itoa(os.Getpid()) + "\n")
	early := SetupEarlySignals()
	logger := logging.New(logging.LevelError)
	set := service.NewServiceSet(logger)
	loop := New(set, logger)
	loop.AdoptSignals(early)
	loop.SetPID1Mode(true)
	go func() {
		time.Sleep(100 * time.Millisecond)
		loop.InitiateShutdown(service.ShutdownReboot)
	}()
	_ = loop.Run(context.Background())
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGHUP} {
		_ = syscall.Kill(os.Getpid(), sig)
		time.Sleep(100 * time.Millisecond)
	}
	os.Stdout.WriteString("PID1-SURVIVED\n")
	os.Exit(0)
}
