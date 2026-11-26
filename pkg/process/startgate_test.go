package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// Nice, rlimits, affinity, scheduling, ioprio and oom_score_adj are set
// on the child from outside after cmd.Start, when it is already
// running: whatever the service forked first ran without them. With
// the runner's start gate, a child forked at once already has them.
//
// Here the old window (microseconds of applyNice against a shell's
// startup) is too small to fail on the old code; functional case 270,
// where the cgroup writes widen it, is the one that showed the race.
// This test checks the gate path itself: the attribute reaches the
// first child and the gate variable does not reach the service.
func TestPostForkAttrsReachTheFirstChild(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not in PATH")
	}
	dir := t.TempDir()
	runner := filepath.Join(dir, "slinit-runner")
	build := exec.Command(gobin, "build", "-o", runner, "../../cmd/slinit-runner")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build runner: %v\n%s", err, out)
	}

	nice := 10
	for i := 0; i < 20; i++ {
		out := filepath.Join(dir, "stat")
		envOut := filepath.Join(dir, "env")
		_, ch, err := StartProcess(ExecParams{
			RunnerPath: runner,
			Nice:       &nice,
			// cat is the shell's first child, forked at once.
			Command: []string{"/bin/sh", "-c", "cat /proc/self/stat > " + out + "; env > " + envOut},
		})
		if err != nil {
			t.Fatalf("StartProcess: %v", err)
		}
		<-ch
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		// Field 19 is nice, counted after the ")" that ends the name.
		f := strings.Fields(string(b[strings.LastIndexByte(string(b), ')')+2:]))
		if f[16] != "10" {
			t.Fatalf("run %d: the service's first child ran at nice %s, want 10", i, f[16])
		}
		env, _ := os.ReadFile(envOut)
		if strings.Contains(string(env), StartGateEnv) {
			t.Fatalf("%s leaked into the service's environment", StartGateEnv)
		}
	}
}

// A bare command name worked for a service slinit started directly
// (exec.Command looks it up in PATH) and failed once the same service
// went through the runner for nice/rlimits/sockets: the runner called
// execve with the bare name. It now looks it up as execvp does.
func TestRunnerResolvesBareCommandName(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not in PATH")
	}
	dir := t.TempDir()
	runner := filepath.Join(dir, "slinit-runner")
	build := exec.Command(gobin, "build", "-o", runner, "../../cmd/slinit-runner")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build runner: %v\n%s", err, out)
	}
	out := filepath.Join(dir, "out")
	nice := 5
	_, ch, err := StartProcess(ExecParams{
		RunnerPath: runner,
		Nice:       &nice,
		Command:    []string{"sh", "-c", "echo $0 > " + out},
	})
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	exit := <-ch
	b, _ := os.ReadFile(out)
	if got := strings.TrimSpace(string(b)); got != "sh" {
		t.Errorf("service did not run with argv[0] sh (exit %v, wrote %q)", exit.Status, got)
	}
}

// rlimit-as and rlimit-data prlimit'ed onto the runner killed it in Go
// runtime init ("failed to reserve page summary memory"), so a service
// with them never started once it went through the runner. They are now
// set by the runner itself, just before its exec.
func TestRunnerServiceWithAddressSpaceLimit(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not in PATH")
	}
	dir := t.TempDir()
	runner := filepath.Join(dir, "slinit-runner")
	build := exec.Command(gobin, "build", "-o", runner, "../../cmd/slinit-runner")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build runner: %v\n%s", err, out)
	}
	out := filepath.Join(dir, "out")
	nice := 5
	_, ch, err := StartProcess(ExecParams{
		RunnerPath: runner,
		Nice:       &nice,
		Rlimits: []Rlimit{
			{Resource: unix.RLIMIT_AS, Soft: 64 << 20, Hard: 64 << 20},
			{Resource: unix.RLIMIT_DATA, Soft: 32 << 20, Hard: 32 << 20},
		},
		Command: []string{"/bin/sh", "-c", "grep -E 'Max (address space|data size)' /proc/self/limits > " + out},
	})
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	exit := <-ch
	b, _ := os.ReadFile(out)
	got := string(b)
	if !strings.Contains(got, "67108864") || !strings.Contains(got, "33554432") {
		t.Errorf("service did not run with its limits (exit %v): %q", exit.Status, got)
	}
}
