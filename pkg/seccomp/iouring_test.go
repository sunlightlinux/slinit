//go:build linux

package seccomp

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/sunlightlinux/slinit/internal/iouringtest"
)

// The opcode table and the opcode-to-syscalls map are transcribed from the
// kernel header and from systemd's io-uring-filter.c. Check both against
// the systemd tree next door, which carries the same header, so a slip in
// the transcription cannot hide.
func TestIOUringTablesMatchSystemd(t *testing.T) {
	root := filepath.Join("..", "..", "..", "systemd")
	hdr, err := os.ReadFile(filepath.Join(root, "src/include/uapi/linux/io_uring.h"))
	if err != nil {
		t.Skipf("systemd tree not found next to slinit: %v", err)
	}
	enum := regexp.MustCompile(`(?s)enum io_uring_op \{(.*?)\};`).FindSubmatch(hdr)
	if enum == nil {
		t.Fatal("enum io_uring_op not found")
	}
	var names []string
	for _, m := range regexp.MustCompile(`IORING_OP_([A-Z0-9_]+)`).FindAllSubmatch(enum[1], -1) {
		if n := strings.ToLower(string(m[1])); n != "last" {
			names = append(names, n)
		}
	}
	if strings.Join(names, ",") != strings.Join(ioUringOps, ",") {
		t.Errorf("opcode table differs from the header:\nheader %v\nslinit %v", names, ioUringOps)
	}

	src, err := os.ReadFile(filepath.Join(root, "src/shared/io-uring-filter.c"))
	if err != nil {
		t.Fatal(err)
	}
	tbl := regexp.MustCompile(`(?s)io_uring_op_syscalls_table\[IORING_OP_LAST\] = \{(.*?)\};`).FindSubmatch(src)
	if tbl == nil {
		t.Fatal("io_uring_op_syscalls_table not found")
	}
	want := map[string]string{}
	for _, m := range regexp.MustCompile(`\[IORING_OP_([A-Z0-9_]+)\]\s*=\s*"([^"]*)"`).FindAllSubmatch(tbl[1], -1) {
		want[strings.ToLower(string(m[1]))] = strings.TrimSuffix(strings.ReplaceAll(string(m[2]), `\0`, ","), ",")
	}
	if len(want) != len(ioUringOpSyscalls) {
		t.Errorf("systemd maps %d operations, slinit %d", len(want), len(ioUringOpSyscalls))
	}
	for op, sc := range want {
		if got := strings.Join(ioUringOpSyscalls[op], ","); got != sc {
			t.Errorf("%s: slinit %q, systemd %q", op, got, sc)
		}
	}
}

func TestFilterDenies(t *testing.T) {
	allow := &Filter{Mode: ModeAllow, Syscalls: []string{"read", "write"}, DefaultAction: ActionKill}
	deny := &Filter{Mode: ModeDeny, Syscalls: []string{"socket"}, DefaultAction: ErrnoAction(unix.EPERM)}
	logOnly := &Filter{Mode: ModeAllow, Syscalls: []string{"read"}, DefaultAction: ActionLog}
	for _, c := range []struct {
		f    *Filter
		name string
		want bool
	}{
		{allow, "read", false}, {allow, "socket", true},
		{deny, "socket", true}, {deny, "read", false},
		{logOnly, "socket", false},
	} {
		if got := c.f.Denies(c.name); got != c.want {
			t.Errorf("mode %v %v: Denies(%s) = %v", c.f.Mode, c.f.Syscalls, c.name, got)
		}
	}
	ops := IOUringOpsDeniedBy(deny)
	sock, _ := IOUringOpNumber("socket")
	if !ops[sock] || len(ops) != 1 {
		t.Errorf("~socket should deny exactly the socket operation, got %v", ops)
	}
	// An allow list without openat refuses the openat operation, which
	// stands for open and openat.
	if o, _ := IOUringOpNumber("openat"); !IOUringOpsDeniedBy(allow)[o] {
		t.Error("an allow list of read+write must refuse IORING_OP_OPENAT")
	}
}

// ---- kernel tests -------------------------------------------------------

const ioUringChildEnv = "SLINIT_TEST_IOURING_CASE"

const exitUnsupported = 42

func init() {
	c := os.Getenv(ioUringChildEnv)
	if c == "" {
		return
	}
	runtime.LockOSThread()
	fail := func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...); os.Exit(1) }
	if err := EnsureNoNewPrivs(); err != nil {
		fail("nnp: %v", err)
	}
	// io_uring itself may be off (kernel.io_uring_disabled) or absent.
	if _, err := iouringtest.Socket(unix.AF_UNIX); err != nil || !IOUringFilterSupported() {
		os.Exit(exitUnsupported)
	}
	want := func(what string, r int32, err error, refused bool) {
		if err != nil {
			fail("%s: %v", what, err)
		}
		if refused && r != -int32(unix.EACCES) {
			fail("%s: result %d, want -EACCES", what, r)
		}
		if !refused && r < 0 {
			fail("%s: result %d, want success", what, r)
		}
	}
	dir := os.Getenv("SLINIT_TEST_DIR")
	switch c {
	case "address-families":
		if err := RestrictIOUringAddressFamilies([]int{unix.AF_UNIX}); err != nil {
			fail("%v", err)
		}
		r, err := iouringtest.Socket(unix.AF_INET)
		want("IORING_OP_SOCKET AF_INET", r, err, true)
		r, err = iouringtest.Socket(unix.AF_UNIX)
		want("IORING_OP_SOCKET AF_UNIX", r, err, false)
	case "suid":
		if err := RestrictIOUringSUIDSGID(); err != nil {
			fail("%v", err)
		}
		r, err := iouringtest.Create(filepath.Join(dir, "suid"), 0o4755)
		want("IORING_OP_OPENAT O_CREAT 04755", r, err, true)
		r, err = iouringtest.Create(filepath.Join(dir, "plain"), 0o644)
		want("IORING_OP_OPENAT O_CREAT 0644", r, err, false)
	case "syscall-filter":
		f, err := Build([]string{"~socket"}, ModeAllow, nil, ErrnoAction(unix.EPERM), nil)
		if err != nil {
			fail("%v", err)
		}
		if err := RestrictIOUringOps(IOUringOpsDeniedBy(f), false); err != nil {
			fail("%v", err)
		}
		r, err := iouringtest.Socket(unix.AF_UNIX)
		want("IORING_OP_SOCKET under ~socket", r, err, true)
		r, err = iouringtest.Create(filepath.Join(dir, "ok"), 0o644)
		want("IORING_OP_OPENAT under ~socket", r, err, false)
	case "allow-list":
		nop, _ := IOUringOpNumber("nop")
		if err := RestrictIOUringOps(map[int]bool{nop: true}, true); err != nil {
			fail("%v", err)
		}
		r, err := iouringtest.One(iouringtest.SQE{Opcode: uint8(nop)})
		want("IORING_OP_NOP, allowed", r, err, false)
		r, err = iouringtest.Create(filepath.Join(dir, "x"), 0o644)
		want("IORING_OP_OPENAT, not on the allow list", r, err, true)
	default:
		fail("unknown case %q", c)
	}
	os.Exit(0)
}

func runIOUringChild(t *testing.T, name string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), ioUringChildEnv+"="+name, "SLINIT_TEST_DIR="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		if ee.ExitCode() == exitUnsupported {
			t.Skip("io_uring or io_uring BPF filters not available on this kernel")
		}
		if ws := ee.Sys().(syscall.WaitStatus); ws.Signaled() {
			t.Fatalf("%s: child killed by %v", name, ws.Signal())
		}
	}
	if err != nil {
		t.Fatalf("%s: %v\n%s", name, err, out)
	}
}

func TestIOUringAddressFamiliesOnKernel(t *testing.T) { runIOUringChild(t, "address-families") }
func TestIOUringSUIDSGIDOnKernel(t *testing.T)        { runIOUringChild(t, "suid") }
func TestIOUringSyscallFilterOnKernel(t *testing.T)   { runIOUringChild(t, "syscall-filter") }
func TestIOUringAllowListOnKernel(t *testing.T)       { runIOUringChild(t, "allow-list") }

func TestParseIOUringFilter(t *testing.T) {
	op := func(n string) int { o, _ := IOUringOpNumber(n); return o }
	for _, c := range []struct {
		in    []string
		ops   []string
		allow bool
	}{
		{[]string{"read write"}, []string{"read", "write"}, true},
		{[]string{"~socket,connect"}, []string{"socket", "connect"}, false},
		{[]string{"read write", "~write"}, []string{"read"}, true},        // other kind removes
		{[]string{"~socket", "socket", "~bind"}, []string{"bind"}, false}, // deny list: allow removes
		{[]string{"read", "", "~socket"}, []string{"socket"}, false},      // empty resets
	} {
		got, allow, err := ParseIOUringFilter(c.in)
		if err != nil {
			t.Fatalf("%v: %v", c.in, err)
		}
		want := map[int]bool{}
		for _, n := range c.ops {
			want[op(n)] = true
		}
		if allow != c.allow || len(got) != len(want) {
			t.Errorf("%v: got %v allow=%v, want %v allow=%v", c.in, got, allow, c.ops, c.allow)
			continue
		}
		for o := range want {
			if !got[o] {
				t.Errorf("%v: missing %s", c.in, ioUringOps[o])
			}
		}
	}
	if _, _, err := ParseIOUringFilter([]string{"~sockett"}); err == nil {
		t.Error("a misspelt operation must be refused, not ignored")
	}
}

func TestParseActionKnowsEveryErrnoName(t *testing.T) {
	for name, want := range map[string]unix.Errno{"EPERM": unix.EPERM, "eafnosupport": unix.EAFNOSUPPORT, "EOPNOTSUPP": unix.EOPNOTSUPP, "ENOTSUP": unix.EOPNOTSUPP, "EWOULDBLOCK": unix.EAGAIN} {
		a, err := ParseAction(name)
		if err != nil || a != ErrnoAction(want) {
			t.Errorf("ParseAction(%s) = %#x, %v; want errno %d", name, uint32(a), err, want)
		}
	}
	if _, err := ParseAction("ENOTANERRNO"); err == nil {
		t.Error("an unknown errno name must be refused")
	}
}
