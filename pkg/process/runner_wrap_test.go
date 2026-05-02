package process

import (
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNeedsRunnerWrap(t *testing.T) {
	cases := []struct {
		name string
		p    ExecParams
		want bool
	}{
		{"empty", ExecParams{}, false},
		{"only mlockall", ExecParams{MlockallFlags: unix.MCL_CURRENT}, true},
		{"only numa", ExecParams{NumaMempolicySet: true, NumaMempolicy: unix.MPOL_BIND}, true},
		{"both", ExecParams{
			MlockallFlags:    unix.MCL_FUTURE,
			NumaMempolicySet: true,
			NumaMempolicy:    unix.MPOL_INTERLEAVE,
		}, true},
		{"sched only", ExecParams{SchedPolicy: unix.SCHED_FIFO}, false},
	}
	for _, c := range cases {
		got := needsRunnerWrap(c.p)
		if got != c.want {
			t.Errorf("%s: needsRunnerWrap = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestWrapWithRunnerArgvShape(t *testing.T) {
	p := ExecParams{
		Command:          []string{"/usr/bin/svc", "--flag", "arg"},
		MlockallFlags:    unix.MCL_CURRENT | unix.MCL_FUTURE, // = 3
		NumaMempolicySet: true,
		NumaMempolicy:    unix.MPOL_BIND,
		NumaNodes:        []uint{0, 2, 4},
		RunnerPath:       "/usr/sbin/slinit-runner",
	}
	got := wrapWithRunner(p)
	want := []string{
		"/usr/sbin/slinit-runner",
		"--mlockall=3",
		"--mempolicy=bind",
		"--numa-nodes=0,2,4",
		"--",
		"/usr/bin/svc",
		"--flag",
		"arg",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wrapWithRunner argv mismatch:\n got %v\nwant %v", got, want)
	}
}

func TestWrapWithRunnerOmitsUnsetFields(t *testing.T) {
	// Only mlockall configured: no --mempolicy/--numa-nodes flags emitted.
	p := ExecParams{
		Command:       []string{"/bin/true"},
		MlockallFlags: unix.MCL_CURRENT,
		RunnerPath:    "/sbin/slinit-runner",
	}
	got := wrapWithRunner(p)
	want := []string{"/sbin/slinit-runner", "--mlockall=1", "--", "/bin/true"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv mismatch:\n got %v\nwant %v", got, want)
	}
}

func TestWrapWithRunnerNumaWithoutNodes(t *testing.T) {
	// Mode=local has no node list — flag must not appear.
	p := ExecParams{
		Command:          []string{"/bin/true"},
		NumaMempolicySet: true,
		NumaMempolicy:    unix.MPOL_LOCAL,
		RunnerPath:       "/sbin/slinit-runner",
	}
	got := wrapWithRunner(p)
	want := []string{"/sbin/slinit-runner", "--mempolicy=local", "--", "/bin/true"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv mismatch:\n got %v\nwant %v", got, want)
	}
}

func TestMempolicyName(t *testing.T) {
	cases := map[uint32]string{
		unix.MPOL_DEFAULT:    "default",
		unix.MPOL_BIND:       "bind",
		unix.MPOL_PREFERRED:  "preferred",
		unix.MPOL_INTERLEAVE: "interleave",
		unix.MPOL_LOCAL:      "local",
	}
	for mode, want := range cases {
		if got := mempolicyName(mode); got != want {
			t.Errorf("mempolicyName(%d) = %q, want %q", mode, got, want)
		}
	}
}
