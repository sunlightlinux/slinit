package shutdown

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestWriteContainerResults(t *testing.T) {
	dir := t.TempDir()
	orig := containerResultsDir
	containerResultsDir = filepath.Join(dir, "results")
	defer func() { containerResultsDir = orig }()

	err := WriteContainerResults(42, service.ShutdownReboot)
	if err != nil {
		t.Fatalf("WriteContainerResults: %v", err)
	}

	// Check exitcode file.
	data, err := os.ReadFile(filepath.Join(containerResultsDir, "exitcode"))
	if err != nil {
		t.Fatalf("read exitcode: %v", err)
	}
	if string(data) != "42" {
		t.Errorf("exitcode = %q, want 42", data)
	}

	// Check haltcode file.
	data, err = os.ReadFile(filepath.Join(containerResultsDir, "haltcode"))
	if err != nil {
		t.Fatalf("read haltcode: %v", err)
	}
	if string(data) != "r" {
		t.Errorf("haltcode = %q, want r", data)
	}
}

func TestReadContainerExitCode(t *testing.T) {
	dir := t.TempDir()
	orig := containerResultsDir
	containerResultsDir = dir
	defer func() { containerResultsDir = orig }()

	// No file → false.
	_, ok := ReadContainerExitCode()
	if ok {
		t.Error("expected false when no exitcode file")
	}

	// Write a code and read it back.
	os.WriteFile(filepath.Join(dir, "exitcode"), []byte("137"), 0644)
	code, ok := ReadContainerExitCode()
	if !ok {
		t.Fatal("expected true after writing exitcode")
	}
	if code != 137 {
		t.Errorf("code = %d, want 137", code)
	}
}

func TestReadContainerHaltCode(t *testing.T) {
	dir := t.TempDir()
	orig := containerResultsDir
	containerResultsDir = dir
	defer func() { containerResultsDir = orig }()

	// No file → empty.
	if hc := ReadContainerHaltCode(); hc != "" {
		t.Errorf("haltcode = %q, want empty", hc)
	}

	os.WriteFile(filepath.Join(dir, "haltcode"), []byte("p"), 0644)
	if hc := ReadContainerHaltCode(); hc != "p" {
		t.Errorf("haltcode = %q, want p", hc)
	}
}

func TestHaltCodes(t *testing.T) {
	cases := []struct {
		st   service.ShutdownType
		want string
	}{
		{service.ShutdownPoweroff, "p"},
		{service.ShutdownReboot, "r"},
		{service.ShutdownHalt, "h"},
		{service.ShutdownSoftReboot, "s"},
		{service.ShutdownKexec, "k"},
		{service.ShutdownNone, "p"}, // default
	}
	for _, tc := range cases {
		got := haltCode(tc.st)
		if got != tc.want {
			t.Errorf("haltCode(%v) = %q, want %q", tc.st, got, tc.want)
		}
	}
}

func TestWriteContainerResultsZeroExit(t *testing.T) {
	dir := t.TempDir()
	orig := containerResultsDir
	containerResultsDir = filepath.Join(dir, "results")
	defer func() { containerResultsDir = orig }()

	err := WriteContainerResults(0, service.ShutdownPoweroff)
	if err != nil {
		t.Fatalf("WriteContainerResults: %v", err)
	}

	code, ok := ReadContainerExitCode()
	if !ok || code != 0 {
		t.Errorf("code = %d, ok = %v, want 0/true", code, ok)
	}
	if hc := ReadContainerHaltCode(); hc != "p" {
		t.Errorf("haltcode = %q, want p", hc)
	}
}

func TestContainerResultsDirOverride(t *testing.T) {
	dir := t.TempDir()
	orig := containerResultsDir
	defer func() { containerResultsDir = orig }()

	custom := filepath.Join(dir, "custom")
	SetContainerResultsDir(custom)
	if ContainerResultsDir() != custom {
		t.Errorf("ContainerResultsDir() = %q, want %q", ContainerResultsDir(), custom)
	}

	err := WriteContainerResults(5, service.ShutdownHalt)
	if err != nil {
		t.Fatalf("WriteContainerResults: %v", err)
	}

	// Verify written to custom path.
	data, _ := os.ReadFile(filepath.Join(custom, "exitcode"))
	if string(data) != "5" {
		t.Errorf("exitcode = %q, want 5", data)
	}
}
