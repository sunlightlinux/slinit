package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func sessionKeyring() int {
	id, _ := unix.KeyctlGetKeyringID(unix.KEY_SPEC_SESSION_KEYRING, true)
	return id
}

// In a child (the test binary again): apply the mode and print the
// session keyring and whether the user keyring is linked into it.
func TestKeyringHelper(t *testing.T) {
	mode := os.Getenv("SLINIT_TEST_KEYRING_MODE")
	if mode == "" {
		t.Skip("helper")
	}
	if err := applyKeyringMode(mode); err != nil {
		fmt.Println("error", err)
		os.Exit(0)
	}
	user, _ := unix.KeyctlGetKeyringID(unix.KEY_SPEC_USER_KEYRING, true)
	buf := make([]byte, 4096)
	n, _ := unix.KeyctlBuffer(unix.KEYCTL_READ, sessionKeyring(), buf, 0)
	linked := false
	for i := 0; i+4 <= n && i+4 <= len(buf); i += 4 {
		if int(binary.NativeEndian.Uint32(buf[i:])) == user {
			linked = true
		}
	}
	fmt.Println("session", sessionKeyring(), "linked", linked)
	os.Exit(0)
}

func TestKeyringMode(t *testing.T) {
	mine := sessionKeyring()
	run := func(mode string) (int, bool) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestKeyringHelper$")
		cmd.Env = append(os.Environ(), "SLINIT_TEST_KEYRING_MODE="+mode)
		out, _ := cmd.Output()
		f := strings.Fields(string(out))
		if len(f) < 4 || f[0] != "session" {
			t.Skipf("keyctl unavailable here: %q", out)
		}
		id, _ := strconv.Atoi(f[1])
		return id, f[3] == "true"
	}
	p, plinked := run("private")
	if p == mine || p == 0 || plinked {
		t.Errorf("private: session %d (mine %d), user keyring linked %v", p, mine, plinked)
	}
	s, slinked := run("shared")
	if s == mine || s == p || !slinked {
		t.Errorf("shared: session %d, user keyring linked %v", s, slinked)
	}
	if err := applyKeyringMode("bogus"); err == nil {
		t.Error("bogus mode accepted")
	}
}
