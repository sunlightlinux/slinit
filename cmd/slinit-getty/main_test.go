package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// TestExpandIssue_Basic: no escapes = passthrough. Regression
// guard against a naive parser that eats every backslash.
func TestExpandIssue_Basic(t *testing.T) {
	in := "Welcome to Alpine Linux\n"
	got := expandIssue(in, "/dev/tty1")
	if got != in {
		t.Errorf("plain issue: got %q, want %q", got, in)
	}
}

// TestExpandIssue_LineEscape: \l → tty basename (no /dev/ prefix).
// Load-bearing for the standard getty(8) \l semantic.
func TestExpandIssue_LineEscape(t *testing.T) {
	got := expandIssue(`login on \l`, "/dev/ttyS0")
	want := "login on ttyS0"
	if got != want {
		t.Errorf("\\l: got %q, want %q", got, want)
	}
}

// TestExpandIssue_UnknownEscape: passes the raw \x through so
// operator-authored ANSI escapes like `\e[1m` survive.
func TestExpandIssue_UnknownEscape(t *testing.T) {
	got := expandIssue(`\e[1mBOLD\e[0m`, "/dev/tty1")
	want := `\e[1mBOLD\e[0m`
	if got != want {
		t.Errorf("unknown escape: got %q, want %q", got, want)
	}
}

// TestExpandIssue_TrailingBackslash: a lone trailing backslash
// with nothing after it must be preserved verbatim (not
// index-out-of-range panic).
func TestExpandIssue_TrailingBackslash(t *testing.T) {
	got := expandIssue(`line\`, "/dev/tty1")
	if got != `line\` {
		t.Errorf("trailing backslash: got %q, want %q", got, `line\`)
	}
}

// TestExpandIssue_TTYWithoutPrefix: bare tty name (no /dev/)
// still surfaces as-is in \l — matches finit's cleanup which
// strips only when the /dev/ prefix is present.
func TestExpandIssue_TTYWithoutPrefix(t *testing.T) {
	got := expandIssue(`on \l`, "ttyS0")
	want := "on ttyS0"
	if got != want {
		t.Errorf("no /dev/ prefix: got %q, want %q", got, want)
	}
}

// TestExpandIssue_KernelEscapes: \s / \r / \m come from
// uname(2). We can't fake the syscall, but we can confirm the
// output contains SOMETHING for each — smoke test only.
func TestExpandIssue_KernelEscapes(t *testing.T) {
	got := expandIssue(`\s \r on \m`, "/dev/tty1")
	// Every field should be non-empty on Linux.
	parts := strings.Fields(got)
	if len(parts) < 3 {
		t.Fatalf("uname substitution produced too few fields: %q", got)
	}
	for i, p := range parts {
		if p == "" {
			t.Errorf("field %d empty in %q", i, got)
		}
	}
}

// TestFirstExisting_None: none of the candidates exist → "".
// Guards the "no login binary, drop to rescue" fallback path.
func TestFirstExisting_None(t *testing.T) {
	got := firstExisting([]string{"/no/such/a", "/no/such/b"})
	if got != "" {
		t.Errorf("all missing: got %q, want empty", got)
	}
}

// TestFirstExisting_PicksFirstExecutable: temp dir with two
// files, only the second is executable. First candidate exists
// but non-exec → skipped. Second (exec) → picked.
func TestFirstExisting_PicksFirstExecutable(t *testing.T) {
	dir := t.TempDir()
	noExec := filepath.Join(dir, "noexec")
	execOK := filepath.Join(dir, "execok")
	if err := os.WriteFile(noExec, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(execOK, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := firstExisting([]string{noExec, execOK})
	if got != execOK {
		t.Errorf("firstExisting: got %q, want %q (non-exec should be skipped)", got, execOK)
	}
}

// TestFirstExisting_SkipsDirectory: a directory at a candidate
// path is not a regular file, must be skipped.
func TestFirstExisting_SkipsDirectory(t *testing.T) {
	dir := t.TempDir()
	got := firstExisting([]string{dir})
	if got != "" {
		t.Errorf("directory candidate: got %q, want empty", got)
	}
}

// TestBaudLookup_Sane: sample of common serial baud rates must
// resolve to termios constants (>0). Regression guard against a
// typo in the map.
func TestBaudLookup_Sane(t *testing.T) {
	for _, baud := range []string{"9600", "38400", "115200", "230400"} {
		if v, ok := baudLookup[baud]; !ok || v == 0 {
			t.Errorf("baudLookup[%q]: ok=%v, val=%d — expected present + non-zero", baud, ok, v)
		}
	}
}

// TestUnixString_TrimsAtNUL: fixed-size C string array with
// trailing NUL padding must trim at first NUL.
func TestUnixString_TrimsAtNUL(t *testing.T) {
	buf := [16]byte{'h', 'i', 0, 'x', 'y', 'z'}
	got := unixString(buf[:])
	if got != "hi" {
		t.Errorf("unixString: got %q, want %q", got, "hi")
	}
}

// TestUnixString_NoNUL: no NUL in the buffer at all — return
// the full string. Edge case for a max-length uname field.
func TestUnixString_NoNUL(t *testing.T) {
	buf := []byte{'a', 'b', 'c'}
	got := unixString(buf)
	if got != "abc" {
		t.Errorf("unixString(no NUL): got %q, want %q", got, "abc")
	}
}

// When login cannot run, getty must not fall back to a shell that asks
// for nothing: only sulogin, which authenticates, may stand in.
func TestRescueIsOnlySulogin(t *testing.T) {
	for _, p := range rescueShellCandidates {
		if filepath.Base(p) != "sulogin" {
			t.Errorf("rescue candidate %s does not authenticate", p)
		}
	}
}

// A login whose exec fails (ETXTBSY during an upgrade) with no sulogin
// present makes getty return an error, so it exits and is respawned,
// and nothing else is executed.
func TestLoginExecFailureExits(t *testing.T) {
	login := filepath.Join(t.TempDir(), "login")
	if err := os.WriteFile(login, []byte("#!/bin/false\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldLogin, oldRescue := loginPathCandidates, rescueShellCandidates
	t.Cleanup(func() { loginPathCandidates, rescueShellCandidates = oldLogin, oldRescue })
	loginPathCandidates = []string{login}
	rescueShellCandidates = []string{filepath.Join(t.TempDir(), "sulogin")}

	var ran []string
	err := execLogin("root", false, func(path string, _ []string, _ []string) error {
		ran = append(ran, path)
		return syscall.ETXTBSY
	})
	if err == nil || !strings.Contains(err.Error(), "text file busy") {
		t.Errorf("err %v, want the exec failure", err)
	}
	if len(ran) != 1 || ran[0] != login {
		t.Errorf("executed %v, want only %s", ran, login)
	}
}

// agetty's modes: the tty group may write to the user's terminal
// (write(1), wall), nobody else may; without a tty group, owner only.
func TestTTYMode(t *testing.T) {
	if m := ttyMode(5); m != 0o620 {
		t.Errorf("with a tty group: %o, want 620", m)
	}
	if m := ttyMode(0); m != 0o600 {
		t.Errorf("without a tty group: %o, want 600", m)
	}
}

// A tty the last session's login gave to its user goes back to root
// before the next prompt. Needs root to chown; skipped otherwise.
func TestReclaimTTY(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to chown a tty")
	}
	ptmx, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	defer unix.Close(ptmx)
	if err := unix.IoctlSetPointerInt(ptmx, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(ptmx, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave := "/dev/pts/" + strconv.Itoa(n)
	fd, err := unix.Open(slave, unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	// As a previous login would have left it.
	if err := unix.Fchown(fd, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	if err := unix.Fchmod(fd, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := reclaimTTY(fd, 5); err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		t.Fatal(err)
	}
	if st.Uid != 0 || st.Gid != 5 || st.Mode&0o777 != 0o620 {
		t.Errorf("%s: uid %d gid %d mode %o, want 0 5 620", slave, st.Uid, st.Gid, st.Mode&0o777)
	}
}
