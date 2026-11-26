package journald

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// freshProcessEnv marks the child inFreshProcess starts.
const freshProcessEnv = "SLINIT_TEST_FRESH_PROCESS"

// inFreshProcess runs the calling test again in a child process and
// returns true in the parent, which must then return. Tests that lower
// RLIMIT_FSIZE to model a full disk need it: the limit holds for every
// file of the process, and when go test can cache the result the
// testing package appends a line to its testlog file for each file the
// tests open. Once the package's other tests have grown that file past
// the limit, its next write fails and the whole package FAILs with
// "can't write testlog.txt: file too large". The child gets no testlog.
func inFreshProcess(t *testing.T) bool {
	t.Helper()
	if os.Getenv(freshProcessEnv) == "1" {
		return false
	}
	cmd := exec.Command(os.Args[0], "-test.run", "^"+regexp.QuoteMeta(t.Name())+"$", "-test.v")
	cmd.Env = append(os.Environ(), freshProcessEnv+"=1")
	out, err := cmd.CombinedOutput()
	switch {
	case err != nil:
		t.Fatalf("%s in a child process: %v\n%s", t.Name(), err, out)
	case strings.Contains(string(out), "--- SKIP: "+t.Name()):
		t.Skipf("skipped in the child process:\n%s", out)
	case !strings.Contains(string(out), "--- PASS: "+t.Name()):
		t.Fatalf("%s did not run in the child process:\n%s", t.Name(), out)
	}
	return true
}
