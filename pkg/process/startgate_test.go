package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
