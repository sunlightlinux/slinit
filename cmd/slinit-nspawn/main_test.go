package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/sunlightlinux/slinit/pkg/seccomp"
)

// TestReexecEnvSentinel — the sentinel string is load-bearing (dispatch
// between parent and child in main()), so if someone renames it a full
// suite of "does slinit-nspawn work at all" tests would go quiet. Lock
// it in.
func TestReexecEnvSentinel(t *testing.T) {
	if reexecEnv == "" {
		t.Fatal("reexecEnv must be a non-empty string")
	}
	if strings.Contains(reexecEnv, " ") {
		t.Errorf("reexecEnv %q contains whitespace — env keys must be shell-safe", reexecEnv)
	}
	if !strings.HasPrefix(reexecEnv, "_") {
		t.Errorf("reexecEnv %q should be underscore-prefixed to signal internal-only", reexecEnv)
	}
}

// TestChildPayloadEnvEncodingRoundtrip — the parent packs
// childPayload.InitArgs into a NUL-separated env var and the child
// unpacks it. If someone changes one side without the other, args
// silently break. Reproduce the pack/unpack directly.
func TestChildPayloadEnvEncodingRoundtrip(t *testing.T) {
	orig := []string{"--config", "/etc/foo.conf", "--", "arg with space"}
	encoded := strings.Join(orig, "\x1f")
	os.Setenv("_SLINIT_NSPAWN_INIT_ARGS", encoded)
	defer os.Unsetenv("_SLINIT_NSPAWN_INIT_ARGS")

	raw := os.Getenv("_SLINIT_NSPAWN_INIT_ARGS")
	got := strings.Split(raw, "\x1f")
	if len(got) != len(orig) {
		t.Fatalf("roundtrip length: got %d, want %d", len(got), len(orig))
	}
	for i, want := range orig {
		if got[i] != want {
			t.Errorf("arg[%d]: got %q, want %q", i, got[i], want)
		}
	}
}

// TestExitCodeSignaled: a container init killed by a signal reports
// 128+signal, as a shell does — not ExitCode()'s -1, which os.Exit
// turned into 255.
func TestExitCodeSignaled(t *testing.T) {
	err := exec.Command("sh", "-c", "kill -TERM $$").Run()
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("sh did not die: %v", err)
	}
	if got := exitCode(ee); got != 128+int(syscall.SIGTERM) {
		t.Errorf("exitCode = %d, want %d", got, 128+int(syscall.SIGTERM))
	}
	err = exec.Command("sh", "-c", "exit 3").Run()
	if ee, ok := err.(*exec.ExitError); !ok || exitCode(ee) != 3 {
		t.Errorf("exit 3: exitCode = %v", err)
	}
}

// TestSeccompDenies: the container ran with no seccomp filter, so the
// calls systemd-nspawn filters out went through. The filter is
// installed in a child test process, which probes calls that work
// unprivileged: mlock (@memlock) and modify_ldt (@cpu-emulation).
// Each is tried before the install, so the probe can tell the filter
// apart from a refusal for other reasons.
func TestSeccompDenies(t *testing.T) {
	if os.Getenv("_NSPAWN_SECCOMP_HELPER") != "" {
		runtime.LockOSThread()
		buf := make([]byte, 4096)
		probe := func(tag string) {
			fmt.Printf("%s mlock %v\n", tag, unix.Mlock(buf))
			_ = unix.Munlock(buf)
			_, _, e := unix.Syscall(unix.SYS_MODIFY_LDT, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
			fmt.Printf("%s modify_ldt %v\n", tag, e)
		}
		probe("before")
		if err := seccomp.EnsureNoNewPrivs(); err != nil {
			fmt.Println("nnp:", err)
			os.Exit(1)
		}
		if err := installSeccomp(); err != nil {
			fmt.Println("install:", err)
			os.Exit(1)
		}
		probe("after")
		os.Exit(0)
	}
	if runtime.GOARCH != "amd64" {
		t.Skip("pkg/seccomp has a system call table for amd64 only")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSeccompDenies$")
	cmd.Env = append(os.Environ(), "_NSPAWN_SECCOMP_HELPER=1")
	out, err := cmd.CombinedOutput()
	t.Logf("helper:\n%s", out)
	if err != nil {
		t.Fatalf("helper: %v", err)
	}
	for _, call := range []string{"mlock", "modify_ldt"} {
		if !strings.Contains(string(out), "before "+call+" <nil>\n") &&
			!strings.Contains(string(out), "before "+call+" errno 0\n") {
			t.Logf("%s fails without the filter here, cannot probe it", call)
			continue
		}
		if !strings.Contains(string(out), "after "+call+" operation not permitted\n") {
			t.Errorf("%s is not refused with EPERM under the container's filter", call)
		}
	}
}

// TestSeccompDenyListCoversNspawn: the calls that reach past the
// container even without the capabilities it drops must be in the
// filter: kexec_load (CAP_SYS_BOOT is kept) and open_by_handle_at
// (CAP_DAC_READ_SEARCH is kept), and the module, raw I/O and clock
// calls systemd-nspawn also filters.
func TestSeccompDenyListCoversNspawn(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("pkg/seccomp has a system call table for amd64 only")
	}
	f, err := seccomp.Build(seccompDeny, seccomp.ModeDeny, nil, seccomp.ErrnoAction(unix.EPERM), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seccomp.Compile(f); err != nil {
		t.Fatal(err)
	}
	for _, call := range []string{"kexec_load", "kexec_file_load", "open_by_handle_at",
		"init_module", "finit_module", "delete_module", "iopl", "ioperm",
		"settimeofday", "clock_settime", "swapon", "bpf", "perf_event_open"} {
		if !slices.Contains(f.Syscalls, call) {
			t.Errorf("%s is not in the container's seccomp deny list", call)
		}
	}
}
