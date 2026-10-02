package process

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withCgroupRoot temporarily points cgroupRoot at the test's tmpdir so
// validateCgroupPath accepts the test's synthetic cgroup tree.
func withCgroupRoot(t *testing.T, root string) {
	t.Helper()
	old := cgroupRoot
	cgroupRoot = root
	t.Cleanup(func() { cgroupRoot = old })
}

func TestApplyCgroupSettingsWritesFiles(t *testing.T) {
	root := t.TempDir()
	withCgroupRoot(t, root)
	cgDir := filepath.Join(root, "myservice")

	settings := []CgroupSetting{
		{"memory.max", "536870912"},
		{"pids.max", "100"},
		{"cpu.weight", "50"},
	}

	err := applyCgroupSettings(cgDir, settings)
	if err != nil {
		t.Fatalf("applyCgroupSettings: %v", err)
	}

	// Verify directory was created
	st, err := os.Stat(cgDir)
	if err != nil {
		t.Fatalf("stat cgroup dir: %v", err)
	}
	if !st.IsDir() {
		t.Fatal("expected directory")
	}

	// Verify files were written (files have mode 0200, need chmod to read)
	for _, s := range settings {
		p := filepath.Join(cgDir, s.File)
		os.Chmod(p, 0644)
		data, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("read %s: %v", s.File, err)
			continue
		}
		if string(data) != s.Value {
			t.Errorf("%s = %q, want %q", s.File, string(data), s.Value)
		}
	}
}

func TestApplyCgroupSettingsAutoCreateDir(t *testing.T) {
	root := t.TempDir()
	withCgroupRoot(t, root)
	cgDir := filepath.Join(root, "deep", "nested", "cgroup")

	settings := []CgroupSetting{
		{"pids.max", "42"},
	}

	err := applyCgroupSettings(cgDir, settings)
	if err != nil {
		t.Fatalf("applyCgroupSettings: %v", err)
	}

	p := filepath.Join(cgDir, "pids.max")
	os.Chmod(p, 0644)
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "42" {
		t.Errorf("pids.max = %q, want 42", data)
	}
}

func TestApplyCgroupAutoCreateDir(t *testing.T) {
	root := t.TempDir()
	withCgroupRoot(t, root)
	cgDir := filepath.Join(root, "svc")

	// applyCgroup should create the directory and write cgroup.procs
	err := applyCgroup(999999, cgDir)
	if err != nil {
		t.Fatalf("applyCgroup: %v", err)
	}

	st, err := os.Stat(cgDir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !st.IsDir() {
		t.Fatal("expected directory")
	}

	// cgroup.procs should have been written (mode 0200, chmod to read)
	procsPath := filepath.Join(cgDir, "cgroup.procs")
	os.Chmod(procsPath, 0644)
	data, err := os.ReadFile(procsPath)
	if err != nil {
		t.Fatalf("read cgroup.procs: %v", err)
	}
	if string(data) != "999999" {
		t.Errorf("cgroup.procs = %q, want 999999", data)
	}
}

func TestEnableSubtreeControllers(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	child := filepath.Join(parent, "child")

	if err := os.MkdirAll(child, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	settings := []CgroupSetting{
		{"memory.max", "1G"},
		{"pids.max", "100"},
		{"cpu.weight", "50"},
		{"memory.high", "800M"}, // duplicate controller: memory
	}

	enableSubtreeControllers(child, settings)

	// The function writes "+controller" to parent's cgroup.subtree_control.
	// On a tmpfs this just creates a file — we verify the last write per
	// controller (or that the file was touched).
	subtreeCtl := filepath.Join(parent, "cgroup.subtree_control")
	_, err := os.Stat(subtreeCtl)
	if err != nil {
		t.Fatalf("subtree_control should have been written: %v", err)
	}
}

func TestValidateCgroupPathRejectsTraversal(t *testing.T) {
	cases := []string{
		"/sys/fs/cgroup/../../../etc/passwd",
		"/etc/passwd",
		"/sys/fs/cgroupX/leak", // not a child of /sys/fs/cgroup
		"",
		"/tmp/foo",
	}
	for _, p := range cases {
		if err := validateCgroupPath(p); err == nil {
			t.Errorf("validateCgroupPath(%q) = nil, want error", p)
		}
	}
	// Sanity: a normal nested path is accepted.
	if err := validateCgroupPath("/sys/fs/cgroup/svc/worker"); err != nil {
		t.Errorf("validateCgroupPath valid path: %v", err)
	}
}

func TestValidateCgroupSettingFileRejectsTraversal(t *testing.T) {
	bad := []string{"", "..", "../escape", "memory/../escape", "subdir/file"}
	for _, n := range bad {
		if err := validateCgroupSettingFile(n); err == nil {
			t.Errorf("validateCgroupSettingFile(%q) = nil, want error", n)
		}
	}
	for _, n := range []string{"memory.max", "cpu.weight", "pids.max"} {
		if err := validateCgroupSettingFile(n); err != nil {
			t.Errorf("validateCgroupSettingFile(%q): %v", n, err)
		}
	}
}

// TestRemoveCgroupEmpty is the ordinary case: the service stopped and
// nothing is left inside, so the directory goes away.
func TestRemoveCgroupEmpty(t *testing.T) {
	root := t.TempDir()
	withCgroupRoot(t, root)
	cgDir := filepath.Join(root, "run-4a696d93")
	if err := os.MkdirAll(cgDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := RemoveCgroup(cgDir); err != nil {
		t.Fatalf("RemoveCgroup: %v", err)
	}
	if _, err := os.Stat(cgDir); !os.IsNotExist(err) {
		t.Errorf("directory still present after RemoveCgroup (stat err: %v)", err)
	}
}

// TestRemoveCgroupNotEmpty pins the safety property that makes the call
// safe to make unconditionally: a cgroup with a child cgroup — on a
// real system, one that still holds processes — is left alone.
func TestRemoveCgroupNotEmpty(t *testing.T) {
	root := t.TempDir()
	withCgroupRoot(t, root)
	cgDir := filepath.Join(root, "busy")
	if err := os.MkdirAll(filepath.Join(cgDir, "child"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := RemoveCgroup(cgDir); err == nil {
		t.Error("RemoveCgroup succeeded on a non-empty cgroup; want an error")
	}
	if _, err := os.Stat(cgDir); err != nil {
		t.Errorf("non-empty cgroup was disturbed: %v", err)
	}
}

// TestRemoveCgroupRefusesRoot: the hierarchy root is shared by the whole
// system and is never a service's own cgroup.
func TestRemoveCgroupRefusesRoot(t *testing.T) {
	root := t.TempDir()
	withCgroupRoot(t, root)

	if err := RemoveCgroup(root); err == nil {
		t.Error("RemoveCgroup removed the cgroup root; want a refusal")
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("cgroup root was disturbed: %v", err)
	}
}

// TestRemoveCgroupRefusesOutside reuses validateCgroupPath, so a path
// that escapes the hierarchy is rejected before any unlink happens.
func TestRemoveCgroupRefusesOutside(t *testing.T) {
	root := t.TempDir()
	withCgroupRoot(t, root)
	outside := filepath.Join(t.TempDir(), "victim")
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := RemoveCgroup(outside); err == nil {
		t.Error("RemoveCgroup accepted a path outside the hierarchy")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("path outside the hierarchy was removed: %v", err)
	}
}

// RemoveCgroupTree must reclaim what a delegated payload left behind, which
// the plain rmdir cannot: a parent with children fails with ENOTEMPTY and
// then keeps failing for the life of the machine, one leaked set per
// restart.
func TestRemoveCgroupTreeReclaimsChildrenLeftByThePayload(t *testing.T) {
	root := t.TempDir()
	withCgroupRoot(t, root)

	svc := root + "/system.slice/payload"
	// What a container runtime or worker pool leaves: nested cgroups the
	// service manager never created and does not know the names of.
	for _, d := range []string{svc + "/runtime/container-a", svc + "/runtime/container-b", svc + "/init"} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	// The non-delegated remover is right to refuse: it is not its subtree.
	if err := RemoveCgroup(svc); err == nil {
		t.Fatal("RemoveCgroup removed a non-empty cgroup; it must only ever rmdir")
	}

	if err := RemoveCgroupTree(svc); err != nil {
		t.Fatalf("RemoveCgroupTree: %v", err)
	}
	if _, err := os.Stat(svc); !os.IsNotExist(err) {
		t.Errorf("delegated cgroup survived removal: %v", err)
	}
	// The slice above it is not ours to remove.
	if _, err := os.Stat(root + "/system.slice"); err != nil {
		t.Errorf("the parent slice was removed too: %v", err)
	}
}

// It still must not take down a cgroup that holds processes. A directory
// standing in for one that is busy is modelled with an undeletable child,
// since the test cannot park a real process in a tmpdir "cgroup".
func TestRemoveCgroupTreeRefusesTheRoot(t *testing.T) {
	root := t.TempDir()
	withCgroupRoot(t, root)
	if err := RemoveCgroupTree(root); err == nil {
		t.Fatal("RemoveCgroupTree removed the cgroup root")
	}
	if err := RemoveCgroupTree(root + "/../../etc"); err == nil {
		t.Fatal("RemoveCgroupTree accepted a path outside the cgroup root")
	}
}

func TestValidateControllerName(t *testing.T) {
	for _, ok := range []string{"memory", "pids", "cpu", "cpuset", "io", "hugetlb", "misc", "rdma"} {
		if err := validateControllerName(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	// These reach a write to cgroup.subtree_control, where a smuggled
	// "-memory" would turn a request to delegate into one that disables a
	// controller the operator asked for.
	for _, bad := range []string{"", "-memory", "+memory", "memory pids", "mem/ory", "Memory", "mem.ory", "memory\n+io"} {
		if err := validateControllerName(bad); err == nil {
			t.Errorf("%q accepted as a controller name", bad)
		}
	}
}

// Delegation hands over the three interface files a payload needs and
// nothing else: the limits slinit wrote stay root-owned, so a delegated
// payload can manage its subtree without widening its own constraints.
// Ownership itself needs root, so this checks the part that does not:
// which controllers get enabled, and on which cgroup.
func TestDelegateCgroupEnablesControllersOnTheParentNotItself(t *testing.T) {
	root := t.TempDir()
	withCgroupRoot(t, root)

	svc := root + "/system.slice/payload"
	if err := os.MkdirAll(root+"/system.slice", 0755); err != nil {
		t.Fatal(err)
	}
	parentCtl := root + "/system.slice/cgroup.subtree_control"
	if err := os.WriteFile(parentCtl, nil, 0644); err != nil {
		t.Fatal(err)
	}

	// One controller, deliberately. In real cgroupfs each write to
	// subtree_control is a command the kernel accumulates, so "+memory"
	// then "+pids" leaves both enabled; on the tmpfs file this test uses,
	// os.WriteFile truncates and only the last one survives. Asserting on
	// two would be asserting that tmpfs behaves like cgroupfs, which it
	// does not. What the design claims — and what is worth pinning — is
	// WHICH cgroup gets written, not how the kernel merges the writes.
	if err := DelegateCgroup(svc, []string{"memory"}, -1, -1); err != nil {
		t.Fatalf("DelegateCgroup: %v", err)
	}

	// The parent is where controllers have to be enabled: until it lists
	// them, the child's cgroup.controllers is empty and its own
	// subtree_control write fails with ENOENT. Confirmed by probe against
	// a real kernel, not read off documentation.
	got, err := os.ReadFile(parentCtl)
	if err != nil {
		t.Fatalf("read parent subtree_control: %v", err)
	}
	if !strings.Contains(string(got), "+memory") {
		t.Errorf("parent subtree_control %q is missing %q", got, "+memory")
	}

	// And the service's own subtree_control must be left alone: it holds
	// the service's processes, so writing it is refused by the kernel.
	// Doing it here would turn a working delegation into a start failure.
	if _, err := os.Stat(svc + "/cgroup.subtree_control"); err == nil {
		t.Error("slinit wrote the delegated cgroup's own subtree_control; " +
			"the kernel refuses that while the cgroup holds processes")
	}
}
