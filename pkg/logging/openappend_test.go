package logging

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpenAppendCreatesAndAppends(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.log")
	for _, s := range []string{"a\n", "b\n"} {
		f, err := OpenAppend(p, 0o640)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(s)
		f.Close()
	}
	got, _ := os.ReadFile(p)
	if string(got) != "a\nb\n" {
		t.Errorf("content %q, want appended a, b", got)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode %v, want 0640", fi.Mode().Perm())
	}
}

// The case systemd closed: a symlink to a file that does not exist yet must
// not make us create that file.
func TestOpenAppendRefusesDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere", "planted")
	os.MkdirAll(filepath.Dir(target), 0o755)
	link := filepath.Join(dir, "x.log")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err := OpenAppend(link, 0o640)
	if !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("got %v, want ELOOP", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Error("the symlink's target was created")
	}
}

// A symlink to an existing file is the administrator's choice and is
// followed.
func TestOpenAppendFollowsLiveSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.log")
	os.WriteFile(target, []byte("x\n"), 0o640)
	link := filepath.Join(dir, "x.log")
	os.Symlink(target, link)
	f, err := OpenAppend(link, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("y\n")
	f.Close()
	if got, _ := os.ReadFile(target); string(got) != "x\ny\n" {
		t.Errorf("target %q, want the line appended", got)
	}
}

// The console the catch-all logger keeps for itself must not reach the
// processes slinit starts. Before F_DUPFD_CLOEXEC it did: a service run in
// container mode found fd 3 open on slinit's original stdout, which under
// PID 1 is /dev/console, writable by any service however unprivileged.
func TestCatchAllConsoleNotInheritedByChildren(t *testing.T) {
	cal, err := StartCatchAll(filepath.Join(t.TempDir(), "c.log"))
	if err != nil {
		t.Fatalf("StartCatchAll: %v", err)
	}
	defer cal.Stop()

	consoleFD := int(cal.Console().Fd())
	want, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(consoleFD))
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("/bin/sh", "-c", "for f in /proc/$$/fd/*; do readlink $f; done; true").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l == want {
			t.Fatalf("child inherited the console fd (%s); child fds:\n%s", want, out)
		}
	}
	flags, err := unix.FcntlInt(uintptr(consoleFD), unix.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}
	if flags&unix.FD_CLOEXEC == 0 {
		t.Error("console fd lacks FD_CLOEXEC")
	}
}
