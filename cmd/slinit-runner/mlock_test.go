package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// buildMlockLib compiles lib/slinit-mlock into dir, or skips without a
// C compiler.
func buildMlockLib(t *testing.T, dir string) string {
	t.Helper()
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler")
	}
	lib := filepath.Join(dir, mlockLibName)
	src := filepath.Join("..", "..", "lib", "slinit-mlock", "slinit-mlock.c")
	if out, err := exec.Command(cc, "-O2", "-U_FORTIFY_SOURCE", "-D_FORTIFY_SOURCE=0", "-fPIC", "-shared", "-o", lib, src).CombinedOutput(); err != nil {
		t.Fatalf("building %s: %v\n%s", mlockLibName, err, out)
	}
	if err := os.Chmod(lib, 0o644); err != nil {
		t.Fatal(err)
	}
	return lib
}

// dynamicTarget finds a dynamically linked system binary.
func dynamicTarget(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"/bin/true", "/usr/bin/true", "/bin/grep", "/usr/bin/grep"} {
		if path, err := resolveInterpreter(p); err == nil && path == p {
			return p
		}
	}
	t.Skip("no system binary found")
	return ""
}

func TestCheckMlockLibRejectsWritable(t *testing.T) {
	lib := filepath.Join(t.TempDir(), mlockLibName)
	if err := os.WriteFile(lib, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkMlockLib(lib); err != nil {
		t.Errorf("0644 library refused: %v", err)
	}
	if err := os.Chmod(lib, 0o664); err != nil {
		t.Fatal(err)
	}
	if err := checkMlockLib(lib); err == nil || !strings.Contains(err.Error(), "writable") {
		t.Errorf("group-writable library accepted: %v", err)
	}
}

func TestFindMlockLibOrder(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(b, mlockLibName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := findMlockLib([]string{filepath.Join(a, mlockLibName), filepath.Join(b, mlockLibName)})
	if err != nil || got != filepath.Join(b, mlockLibName) {
		t.Errorf("findMlockLib = %q, %v", got, err)
	}
	if _, err := findMlockLib([]string{filepath.Join(a, mlockLibName)}); err == nil {
		t.Error("missing library not reported")
	}
}

func TestCheckPreloadTarget(t *testing.T) {
	dir := t.TempDir()
	lib := buildMlockLib(t, dir)
	target := dynamicTarget(t)

	if err := checkPreloadTarget(target, lib); err != nil {
		t.Errorf("dynamic %s refused: %v", target, err)
	}

	// A script is judged by its interpreter.
	script := filepath.Join(dir, "script")
	if err := os.WriteFile(script, []byte("#!"+target+" -x\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkPreloadTarget(script, lib); err != nil {
		t.Errorf("script with dynamic interpreter refused: %v", err)
	}

	// Not an ELF at all.
	junk := filepath.Join(dir, "junk")
	if err := os.WriteFile(junk, []byte("not elf\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkPreloadTarget(junk, lib); err == nil {
		t.Error("non-ELF target accepted")
	}

	// setuid: the loader ignores LD_PRELOAD paths in secure execution.
	suid := filepath.Join(dir, "suid")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(suid, data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(suid, os.ModeSetuid|0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkPreloadTarget(suid, lib); err == nil || !strings.Contains(err.Error(), "setuid") {
		t.Errorf("setuid target accepted: %v", err)
	}
}

// A static binary has no loader, so LD_PRELOAD would be ignored.
func TestCheckPreloadTargetRejectsStatic(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain")
	}
	dir := t.TempDir()
	lib := buildMlockLib(t, dir)
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	static := filepath.Join(dir, "static")
	cmd := exec.Command(gobin, "build", "-o", static, src)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	if err := checkPreloadTarget(static, lib); err == nil || !strings.Contains(err.Error(), "statically linked") {
		t.Errorf("static target accepted: %v", err)
	}
}

// End to end: the runner, with the library beside it, leaves the exec'd
// program with locked memory — the thing the runner's own mlockall could
// never do.
func TestRunnerLocksServiceMemory(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain")
	}
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_MEMLOCK, &lim); err == nil &&
		os.Geteuid() != 0 && lim.Cur != unix.RLIM_INFINITY && lim.Cur < 64<<20 {
		t.Skip("RLIMIT_MEMLOCK too low to lock a process without privileges")
	}
	grep, err := exec.LookPath("grep")
	if err != nil {
		t.Skip("no grep")
	}
	dir := t.TempDir()
	buildMlockLib(t, dir)
	runner := filepath.Join(dir, "slinit-runner")
	if out, err := exec.Command(gobin, "build", "-o", runner, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	out, err := exec.Command(runner, "--mlockall="+strconv.Itoa(unix.MCL_CURRENT),
		"--", grep, "VmLck", "/proc/self/status").CombinedOutput()
	if err != nil {
		t.Fatalf("runner: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`VmLck:\s+(\d+) kB`).FindSubmatch(out)
	if m == nil {
		t.Fatalf("no VmLck line in %q", out)
	}
	if kb, _ := strconv.Atoi(string(m[1])); kb == 0 {
		t.Errorf("service memory not locked: %s", m[0])
	}

	// The lock follows the main process through exec: a shell that
	// execs the real program leaves that program locked.
	out, err = exec.Command(runner, "--mlockall="+strconv.Itoa(unix.MCL_CURRENT),
		"--", "/bin/sh", "-c", "exec "+grep+" VmLck /proc/self/status").CombinedOutput()
	if err != nil {
		t.Fatalf("runner (exec chain): %v\n%s", err, out)
	}
	if m := regexp.MustCompile(`VmLck:\s+(\d+) kB`).FindSubmatch(out); m == nil || string(m[1]) == "0" {
		t.Errorf("program exec'd by the service not locked: %q", out)
	}

	// A child the service forks is neither locked nor left with the
	// preload variables.
	out, err = exec.Command(runner, "--mlockall="+strconv.Itoa(unix.MCL_CURRENT),
		"--", "/bin/sh", "-c", grep+" -E 'VmLck' /proc/self/status; /usr/bin/env; true").CombinedOutput()
	if err != nil {
		t.Fatalf("runner (forked child): %v\n%s", err, out)
	}
	if m := regexp.MustCompile(`VmLck:\s+(\d+) kB`).FindSubmatch(out); m == nil || string(m[1]) != "0" {
		t.Errorf("forked child locked: %q", out)
	}
	if strings.Contains(string(out), "SLINIT_MLOCKALL") || strings.Contains(string(out), mlockLibName) {
		t.Errorf("preload variables leaked to a forked child:\n%s", out)
	}
}
