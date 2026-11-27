package process

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// The rlimits added in 4.1 (nproc, rttime, nice, ...) go the same way
// as the old five: set from outside behind the start gate, before the
// service's own code runs.
func TestMoreRlimitsReachTheService(t *testing.T) {
	out := filepath.Join(t.TempDir(), "limits")
	_, ch, err := StartProcess(ExecParams{
		Rlimits: []Rlimit{
			{Resource: unix.RLIMIT_NPROC, Soft: 4000, Hard: 4001},
			{Resource: unix.RLIMIT_RTTIME, Soft: 500000, Hard: 500000},
			{Resource: unix.RLIMIT_NICE, Soft: 0, Hard: 0},
		},
		Command: []string{"/bin/sh", "-c", "cat /proc/self/limits > " + out},
	})
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	<-ch
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]string{
		{"Max processes", "4000", "4001"},
		{"Max realtime timeout", "500000", "500000"},
		{"Max nice priority", "0", "0"},
	} {
		found := false
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(strings.TrimPrefix(line, want[0]))
			if strings.HasPrefix(line, want[0]) && len(f) >= 2 && f[0] == want[1] && f[1] == want[2] {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is not %s %s:\n%s", want[0], want[1], want[2], b)
		}
	}
}
