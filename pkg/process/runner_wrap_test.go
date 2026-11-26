package process

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// TestWrapWithRunnerSupplementaryGIDs verifies each configured
// supplementary GID becomes its own --supp-gid=N runner flag, and
// that the emit is gated on a run-as drop being present (no UID drop
// means no setgroups path in the runner).
func TestWrapWithRunnerSupplementaryGIDs(t *testing.T) {
	p := ExecParams{
		Command:           []string{"/bin/true"},
		MlockallFlags:     unix.MCL_CURRENT,
		RunnerPath:        "/sbin/slinit-runner",
		RunAsUID:          1000,
		RunAsGID:          1000,
		SupplementaryGIDs: []uint32{27, 100, 500},
	}
	got := wrapWithRunner(p)
	want := []string{
		"/sbin/slinit-runner",
		"--mlockall=1",
		"--run-as-uid=1000",
		"--run-as-gid=1000",
		"--supp-gid=27",
		"--supp-gid=100",
		"--supp-gid=500",
		"--",
		"/bin/true",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wrapWithRunner argv mismatch:\n got %v\nwant %v", got, want)
	}
}

// TestWrapWithRunnerSupplementaryGIDsIgnoredWithoutRunAs pins the
// current behaviour: without a UID/GID drop, supplementary groups
// are not plumbed to the runner. The runner's setgroups block is
// gated behind the same run-as check, so this keeps the two sides
// consistent — no orphaned flag emit that the runner would ignore.
func TestWrapWithRunnerSupplementaryGIDsIgnoredWithoutRunAs(t *testing.T) {
	p := ExecParams{
		Command:           []string{"/bin/true"},
		MlockallFlags:     unix.MCL_CURRENT,
		RunnerPath:        "/sbin/slinit-runner",
		SupplementaryGIDs: []uint32{27, 100},
	}
	got := wrapWithRunner(p)
	for _, a := range got {
		if len(a) >= 10 && a[:10] == "--supp-gid" {
			t.Errorf("unexpected supp-gid flag with no run-as drop: %v", got)
		}
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

// securebits must be set in the task that becomes the service, which is
// the runner: the parent-side path was a stub that always failed.
func TestWrapWithRunnerSecurebits(t *testing.T) {
	p := ExecParams{
		Command:    []string{"/usr/bin/svc"},
		Securebits: SecbitNoroot | SecbitKeepCaps,
		RunnerPath: "/usr/sbin/slinit-runner",
	}
	if !needsRunnerWrap(p) {
		t.Fatal("securebits alone does not wrap the command in the runner")
	}
	want := []string{"/usr/sbin/slinit-runner", "--securebits=17", "--", "/usr/bin/svc"}
	if got := wrapWithRunner(p); !reflect.DeepEqual(got, want) {
		t.Errorf("wrapWithRunner argv mismatch:\n got %v\nwant %v", got, want)
	}
	// Securebits are confinement: without the runner the service must not
	// start with them silently dropped.
	missing := runnerSecurityFeatures(p)
	if len(missing) != 1 || missing[0] != "securebits" {
		t.Errorf("runnerSecurityFeatures = %v, want [securebits]", missing)
	}
}

// chroot is handed to the runner rather than applied at fork. A chroot
// at fork made the kernel resolve the runner's own path inside the
// jail: the service failed to start, or ran whatever the jail held at
// that path, as root and unconfined.
func TestWrapWithRunnerChroot(t *testing.T) {
	p := ExecParams{
		RunnerPath: "/sbin/slinit-runner",
		Command:    []string{"/bin/sh"},
		NoNewPrivs: true,
		Chroot:     "/srv/jail",
		WorkingDir: "/work",
	}
	argv := wrapWithRunner(p)
	want := []string{"--chroot=/srv/jail", "--chdir=/work", "--", "/bin/sh"}
	if got := argv[len(argv)-len(want):]; !reflect.DeepEqual(got, want) {
		t.Errorf("argv tail = %q, want %q", got, want)
	}

	p.WorkingDir = ""
	for _, a := range wrapWithRunner(p) {
		if strings.HasPrefix(a, "--chdir") {
			t.Errorf("--chdir emitted without a working-dir: %q", a)
		}
	}
	// Without chroot the runner still enters working-dir itself, after
	// dropping to the service's user.
	p.Chroot = ""
	p.WorkingDir = "/work"
	sawChdir := false
	for _, a := range wrapWithRunner(p) {
		if strings.HasPrefix(a, "--chroot") {
			t.Errorf("chroot flag emitted without chroot: %q", a)
		}
		sawChdir = sawChdir || a == "--chdir=/work"
	}
	if !sawChdir {
		t.Error("--chdir missing for a wrapped service with working-dir")
	}
}

// End to end through StartProcess with a stand-in runner that records
// its arguments. With the chroot applied at fork, this unprivileged
// test could not even start it (chroot needs CAP_SYS_CHROOT); as root,
// the kernel would have looked the runner up inside the jail.
func TestStartProcessChrootGoesToRunner(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "args")
	runner := filepath.Join(dir, "runner")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + out + "\n"
	if err := os.WriteFile(runner, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	jail := t.TempDir()

	_, ch, err := StartProcess(ExecParams{
		RunnerPath: runner,
		Command:    []string{"/bin/true"},
		NoNewPrivs: true,
		Chroot:     jail,
		WorkingDir: "/work",
	})
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	<-ch
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the runner did not run: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(b)), "\n")
	want := []string{"--chroot=" + jail, "--chdir=/work", "--", "/bin/true"}
	if tail := got[len(got)-len(want):]; !reflect.DeepEqual(tail, want) {
		t.Errorf("runner args tail = %q, want %q", tail, want)
	}
}
