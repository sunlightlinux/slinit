package process

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyCgroupSettingsWritesFiles(t *testing.T) {
	root := t.TempDir()
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
