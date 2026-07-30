package journal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsValidID(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"", false},
		{"abc", false},
		{"0123456789abcdef0123456789abcdef", true},         // 32 lowercase hex
		{"0123456789ABCDEF0123456789ABCDEF", false},        // uppercase rejected
		{"0123-4567-89ab-cdef-0123-4567-89ab-cdef", false}, // dashes rejected
		{"0123456789abcdef0123456789abcde", false},         // 31 chars
		{"0123456789abcdef0123456789abcdef0", false},       // 33 chars
		{"0123456789abcdefzzzzzzzzzzzzzzzz", false},        // invalid hex chars
	}
	for _, c := range cases {
		if got := isValidID(c.s); got != c.want {
			t.Errorf("isValidID(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}

func TestNewRandomID(t *testing.T) {
	// Generate a few IDs, verify they're all valid and distinct.
	seen := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		id, err := newRandomID()
		if err != nil {
			t.Fatalf("newRandomID: %v", err)
		}
		if !isValidID(id) {
			t.Errorf("newRandomID produced invalid ID: %q", id)
		}
		if _, dup := seen[id]; dup {
			t.Errorf("newRandomID collision after %d generations: %q", i, id)
		}
		seen[id] = struct{}{}
	}
}

func TestReadWriteIDFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "id")

	id := "abcdef0123456789abcdef0123456789"
	if err := writeIDFile(path, id); err != nil {
		t.Fatalf("writeIDFile: %v", err)
	}

	got, err := readIDFile(path)
	if err != nil {
		t.Fatalf("readIDFile: %v", err)
	}
	if got != id {
		t.Errorf("round-trip: got %q, want %q", got, id)
	}

	// Verify file contents include trailing newline (systemd convention).
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if buf[len(buf)-1] != '\n' {
		t.Errorf("writeIDFile should terminate with newline, got %q", buf)
	}
}

func TestReadIDFileRejectsMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad")

	if err := os.WriteFile(path, []byte("not-a-valid-id\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readIDFile(path); err == nil {
		t.Errorf("readIDFile should reject malformed content")
	}
}

func TestReadIDFileToleratesWhitespace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spaces")

	id := "abcdef0123456789abcdef0123456789"
	// Trailing whitespace + newlines — systemd's own machine-id files
	// sometimes carry either.
	if err := os.WriteFile(path, []byte("  "+id+"\n\n  "), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readIDFile(path)
	if err != nil {
		t.Fatalf("readIDFile: %v", err)
	}
	if got != id {
		t.Errorf("whitespace stripping failed: got %q, want %q", got, id)
	}
}

func TestInitIDsCachesAndReturns(t *testing.T) {
	resetIDsForTest()
	defer resetIDsForTest()

	// Point BootID + MachineID paths at a temp dir via env override
	// wouldn't work — the constants are package-level. So this test
	// exercises the InitIDs happy path with whatever /etc/machine-id +
	// /run/slinit/boot-id look like on the test host. That's less
	// deterministic but still exercises the cache + getters.
	//
	// The alternative — refactor to inject paths — is larger surface
	// area we don't need for a single-shot Init call.
	if err := InitIDs("test-host"); err != nil {
		// On a container without machine-id it still succeeds by
		// falling back to a transient ID; we only fail on true
		// permission errors, which the sandbox shouldn't hit.
		t.Skipf("InitIDs failed in this environment: %v", err)
	}

	boot := BootID()
	if !isValidID(boot) {
		t.Errorf("BootID returned malformed value: %q", boot)
	}

	machine := MachineID()
	if !isValidID(machine) {
		t.Errorf("MachineID returned malformed value: %q", machine)
	}

	if h := Hostname(); h != "test-host" {
		t.Errorf("Hostname: got %q, want %q", h, "test-host")
	}

	// Second InitIDs should be a no-op (values stay the same).
	prevBoot, prevMachine := boot, machine
	if err := InitIDs("different-host"); err != nil {
		t.Fatalf("second InitIDs: %v", err)
	}
	if BootID() != prevBoot || MachineID() != prevMachine {
		t.Errorf("second InitIDs should not change cached values")
	}
	if Hostname() != "test-host" {
		t.Errorf("second InitIDs changed hostname unexpectedly: got %q", Hostname())
	}
}

func TestBootIDPanicsBeforeInit(t *testing.T) {
	resetIDsForTest()
	defer resetIDsForTest()

	defer func() {
		if r := recover(); r == nil {
			t.Errorf("BootID before InitIDs should panic")
		}
	}()
	_ = BootID()
}
