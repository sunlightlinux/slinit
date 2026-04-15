package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfigValidates(t *testing.T) {
	c := DefaultConfig()
	if err := c.Validate(); err != nil {
		t.Fatalf("default config should validate: %v", err)
	}
}

func TestValidateRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Config)
	}{
		{"empty output dir", func(c *Config) { c.OutputDir = "" }},
		{"empty boot name", func(c *Config) { c.BootServiceName = "" }},
		{"negative gettys", func(c *Config) { c.GettyCount = -1 }},
		{"silly getty count", func(c *Config) { c.GettyCount = 1000 }},
		{"missing getty binary", func(c *Config) { c.GettyCmd = ""; c.GettyCount = 2 }},
	}
	for _, tc := range cases {
		c := DefaultConfig()
		tc.mut(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected validation error", tc.name)
		}
	}
}

func TestPlanContainsExpectedFiles(t *testing.T) {
	c := DefaultConfig()
	c.OutputDir = "/tmp/slinit-test"
	c.GettyCount = 2
	c.WithNetwork = true
	c.WithShutdownHook = true

	plan, err := Plan(c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	want := []string{
		"README.md",
		"boot",
		"env",
		"getty-tty1",
		"getty-tty2",
		"network",
		"shutdown-hook.sample",
		"system-init",
		"system-mounts",
	}
	if len(plan) != len(want) {
		t.Fatalf("plan has %d entries, want %d: %+v", len(plan), len(want), pathsOf(plan))
	}
	for i, p := range plan {
		if p.path != want[i] {
			t.Errorf("plan[%d] = %q, want %q", i, p.path, want[i])
		}
	}
}

func TestPlanRespectsWithFlags(t *testing.T) {
	c := DefaultConfig()
	c.GettyCount = 0
	c.WithMounts = false
	c.WithNetwork = false
	c.WithShutdownHook = false

	plan, err := Plan(c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, p := range plan {
		switch p.path {
		case "system-mounts", "network", "shutdown-hook.sample":
			t.Errorf("plan unexpectedly includes %q", p.path)
		}
		if strings.HasPrefix(p.path, "getty-") {
			t.Errorf("plan unexpectedly includes getty entry %q", p.path)
		}
	}
}

func TestBootServiceContainsWaitsForEveryTarget(t *testing.T) {
	c := DefaultConfig()
	c.GettyCount = 3
	c.WithMounts = true
	c.WithNetwork = true

	plan, err := Plan(c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	var bootBody string
	for _, f := range plan {
		if f.path == c.BootServiceName {
			bootBody = f.body
			break
		}
	}
	if bootBody == "" {
		t.Fatal("boot service body not found")
	}

	// Every real target should appear as a waits-for entry.
	wants := []string{
		"waits-for: system-init",
		"waits-for: system-mounts",
		"waits-for: network",
		"waits-for: getty-tty1",
		"waits-for: getty-tty2",
		"waits-for: getty-tty3",
		"type = internal",
	}
	for _, w := range wants {
		if !strings.Contains(bootBody, w) {
			t.Errorf("boot service missing %q\nbody:\n%s", w, bootBody)
		}
	}
}

func TestGettyBodyReferencesTTY(t *testing.T) {
	c := DefaultConfig()
	c.GettyCount = 1
	c.GettyCmd = "/sbin/mingetty"
	c.GettyBaud = 9600

	plan, err := Plan(c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var body string
	for _, f := range plan {
		if f.path == "getty-tty1" {
			body = f.body
		}
	}
	if !strings.Contains(body, "/sbin/mingetty") {
		t.Errorf("getty body missing binary: %s", body)
	}
	if !strings.Contains(body, "--keep-baud 9600") {
		t.Errorf("getty body missing baudrate: %s", body)
	}
	if !strings.Contains(body, "tty1 linux") {
		t.Errorf("getty body missing tty arg: %s", body)
	}
	if !strings.Contains(body, "inittab-line = tty1") {
		t.Errorf("getty body missing inittab-line: %s", body)
	}
	// Regression guard: inittab-id must be "t<index>" (at most 4 chars for
	// utmpx), not accidentally "ttty1" from a double "tty" prefix.
	if !strings.Contains(body, "inittab-id = t1") {
		t.Errorf("getty body missing inittab-id = t1: %s", body)
	}
	if strings.Contains(body, "inittab-id = ttty") {
		t.Errorf("getty body has double-tty inittab-id: %s", body)
	}
}

func TestEnvFileIncludesHostnameAndTimezone(t *testing.T) {
	c := DefaultConfig()
	c.Hostname = "node42"
	c.Timezone = "Europe/Bucharest"

	plan, err := Plan(c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var env string
	for _, f := range plan {
		if f.path == "env" {
			env = f.body
		}
	}
	if !strings.Contains(env, "HOSTNAME=node42") {
		t.Errorf("env missing HOSTNAME: %s", env)
	}
	if !strings.Contains(env, "TZ=Europe/Bucharest") {
		t.Errorf("env missing TZ: %s", env)
	}
	if !strings.Contains(env, "PATH=") {
		t.Errorf("env missing PATH: %s", env)
	}
}

func TestEnvFileOmitsEmptyHostnameAndTZ(t *testing.T) {
	c := DefaultConfig()
	c.Hostname = ""
	c.Timezone = ""

	plan, err := Plan(c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var env string
	for _, f := range plan {
		if f.path == "env" {
			env = f.body
		}
	}
	if strings.Contains(env, "HOSTNAME=") {
		t.Errorf("env should omit HOSTNAME when empty: %s", env)
	}
	if strings.Contains(env, "TZ=") {
		t.Errorf("env should omit TZ when empty: %s", env)
	}
}

func TestWriteAllWritesFiles(t *testing.T) {
	dir := t.TempDir()
	c := DefaultConfig()
	c.OutputDir = filepath.Join(dir, "boot.d")
	c.GettyCount = 2

	plan, err := Plan(c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	written, err := WriteAll(c, plan)
	if err != nil {
		t.Fatalf("WriteAll: %v", err)
	}
	if len(written) != len(plan) {
		t.Errorf("written %d, plan %d", len(written), len(plan))
	}
	for _, p := range written {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("missing file %s: %v", p, err)
		}
	}

	// Service file should parse sanely — check that at least one key=value
	// line exists.
	body, err := os.ReadFile(filepath.Join(c.OutputDir, "system-init"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "type = internal") {
		t.Errorf("system-init body malformed:\n%s", body)
	}
}

func TestWriteAllRefusesExistingWithoutForce(t *testing.T) {
	dir := t.TempDir()
	c := DefaultConfig()
	c.OutputDir = dir
	c.GettyCount = 0
	c.WithMounts = false

	// Pre-create a file the generator would also create.
	if err := os.WriteFile(filepath.Join(dir, "boot"), []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}

	plan, err := Plan(c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, err := WriteAll(c, plan); err == nil {
		t.Fatal("expected error overwriting existing file without --force")
	}

	// Retry with force: should succeed and replace the file.
	c.Force = true
	plan, err = Plan(c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, err := WriteAll(c, plan); err != nil {
		t.Fatalf("WriteAll with --force: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "boot"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) == "existing" {
		t.Error("file was not overwritten with --force")
	}
}

func TestWriteAllIsAtomic(t *testing.T) {
	// After a successful run, no leftover .tmp files should remain.
	dir := t.TempDir()
	c := DefaultConfig()
	c.OutputDir = dir
	c.GettyCount = 1

	plan, err := Plan(c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, err := WriteAll(c, plan); err != nil {
		t.Fatalf("WriteAll: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestRunCLI_DryRun(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "boot.d")

	// Capture stdout/stderr via temp files since run() takes *os.File.
	outF, err := os.CreateTemp(dir, "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer outF.Close()
	errF, err := os.CreateTemp(dir, "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer errF.Close()

	code := run([]string{"-n", "-d", target, "-t", "1"}, outF, errF)
	if code != 0 {
		t.Fatalf("run exit = %d, want 0", code)
	}
	// Dry run must not create the output directory.
	if _, err := os.Stat(target); err == nil {
		t.Errorf("dry run created output dir %s", target)
	}

	out, _ := os.ReadFile(outF.Name())
	if !strings.Contains(string(out), "dry run") {
		t.Errorf("dry-run output missing marker:\n%s", out)
	}
}

func TestRunCLI_WriteSuccess(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "boot.d")

	outF, _ := os.CreateTemp(dir, "stdout")
	defer outF.Close()
	errF, _ := os.CreateTemp(dir, "stderr")
	defer errF.Close()

	code := run([]string{"-d", target, "-t", "2", "--hostname", "testhost"}, outF, errF)
	if code != 0 {
		errOut, _ := os.ReadFile(errF.Name())
		t.Fatalf("run exit = %d, stderr: %s", code, errOut)
	}
	// Verify a couple of files landed in place.
	for _, f := range []string{"boot", "system-init", "getty-tty1", "getty-tty2", "env"} {
		if _, err := os.Stat(filepath.Join(target, f)); err != nil {
			t.Errorf("missing file %s: %v", f, err)
		}
	}
	env, _ := os.ReadFile(filepath.Join(target, "env"))
	if !strings.Contains(string(env), "HOSTNAME=testhost") {
		t.Errorf("env missing HOSTNAME: %s", env)
	}
}

func TestRunCLI_InvalidFlag(t *testing.T) {
	dir := t.TempDir()
	outF, _ := os.CreateTemp(dir, "stdout")
	defer outF.Close()
	errF, _ := os.CreateTemp(dir, "stderr")
	defer errF.Close()

	code := run([]string{"-t", "-5"}, outF, errF)
	if code == 0 {
		t.Error("expected non-zero exit on invalid getty count")
	}
}

// Helper for test diagnostics.
func pathsOf(files []generatedFile) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.path
	}
	return out
}
