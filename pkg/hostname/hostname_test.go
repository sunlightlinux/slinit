package hostname

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMachineInfo_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "machine-info")
	restore := swapPath(&MachineInfoPath, path)
	defer restore()

	// Author-style seed with a comment we must preserve.
	seed := "# hand-written comment\n" +
		"PRETTY_HOSTNAME=\"Ionut's Laptop\"\n" +
		"ICON_NAME=computer-laptop\n" +
		"CHASSIS=laptop\n" +
		"# trailing comment\n"
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	mi, err := LoadMachineInfo()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := mi.Get("PRETTY_HOSTNAME"); got != "Ionut's Laptop" {
		t.Errorf("PRETTY_HOSTNAME = %q, want Ionut's Laptop", got)
	}
	if got := mi.Get("ICON_NAME"); got != "computer-laptop" {
		t.Errorf("ICON_NAME = %q", got)
	}
	// Overwrite existing key + add a new one.
	mi.Set("PRETTY_HOSTNAME", "Ceres VM")
	mi.Set("DEPLOYMENT", "production")
	if err := mi.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	txt := string(got)
	for _, want := range []string{
		"# hand-written comment",
		"# trailing comment",
		`PRETTY_HOSTNAME="Ceres VM"`,
		"ICON_NAME=computer-laptop",
		"CHASSIS=laptop",
		"DEPLOYMENT=production",
	} {
		if !strings.Contains(txt, want) {
			t.Errorf("missing %q in:\n%s", want, txt)
		}
	}
}

// A value is one line: a control character in one would let a caller
// write a second assignment, or a line the parser reads differently, into
// the file. Set records the refusal and Save reports it, writing nothing.
func TestMachineInfo_RefusesControlCharacters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(path, []byte("A=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"x\nB=2", "x\rB=2", "x\x1b[2J", "x\x7f"} {
		mi, err := LoadEnvFile(path)
		if err != nil {
			t.Fatal(err)
		}
		mi.Set("A", v)
		if err := mi.Save(); err == nil {
			t.Errorf("Save after Set(A, %q) succeeded", v)
		}
		if b, _ := os.ReadFile(path); string(b) != "A=1\n" {
			t.Errorf("Set(A, %q) wrote:\n%s", v, b)
			os.WriteFile(path, []byte("A=1\n"), 0o644)
		}
	}
	mi, _ := LoadEnvFile(path)
	mi.Set("A", "tab\tis fine")
	if err := mi.Save(); err != nil {
		t.Errorf("a tab refused: %v", err)
	}
}

func TestMachineInfo_DeleteViaEmptySet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "machine-info")
	restore := swapPath(&MachineInfoPath, path)
	defer restore()

	if err := os.WriteFile(path, []byte("CHASSIS=laptop\nICON_NAME=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mi, err := LoadMachineInfo()
	if err != nil {
		t.Fatal(err)
	}
	mi.Set("CHASSIS", "") // delete
	if err := mi.Save(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), "CHASSIS") {
		t.Errorf("CHASSIS not removed:\n%s", got)
	}
	if !strings.Contains(string(got), "ICON_NAME=x") {
		t.Errorf("ICON_NAME dropped:\n%s", got)
	}
}

func TestDecodeValue(t *testing.T) {
	cases := map[string]string{
		`bare`:                    `bare`,
		`"double quoted"`:         `double quoted`,
		`'single quoted'`:         `single quoted`,
		`"esc \"inner\""`:         `esc "inner"`,
		`"back \\ slash"`:         `back \ slash`,
		`bare # trailing comment`: `bare`,
	}
	for in, want := range cases {
		if got := decodeValue(in); got != want {
			t.Errorf("decodeValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEncodeValue(t *testing.T) {
	cases := map[string]string{
		`plain`:            `plain`,
		`has space`:        `"has space"`,
		`has"quote`:        `"has\"quote"`,
		`has$dollar`:       `"has\$dollar"`,
		`has#hash`:         `"has#hash"`,
		`no-issue-here.42`: `no-issue-here.42`,
	}
	for in, want := range cases {
		if got := encodeValue(in); got != want {
			t.Errorf("encodeValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChassisFromSMBIOS(t *testing.T) {
	cases := map[int]string{
		3:  "desktop",
		7:  "desktop",
		9:  "laptop",
		10: "laptop",
		17: "server",
		23: "server",
		30: "tablet",
		31: "convertible",
		1:  "", // Other → unmapped
		2:  "", // Unknown → unmapped
	}
	for in, want := range cases {
		if got := chassisFromSMBIOS(in); got != want {
			t.Errorf("chassisFromSMBIOS(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestValidChassis(t *testing.T) {
	for _, ok := range []string{"desktop", "laptop", "convertible", "server", "tablet", "vm", "container"} {
		if !ValidChassis(ok) {
			t.Errorf("ValidChassis(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "phone", "workstation", "SERVER"} {
		if ValidChassis(bad) {
			t.Errorf("ValidChassis(%q) = true", bad)
		}
	}
}

func TestParseOSRelease(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "os-release")
	os.WriteFile(path, []byte(
		"NAME=\"Sunlight OS\"\n"+
			"PRETTY_NAME=\"Sunlight OS 2.2\"\n"+
			"# a comment\n"+
			"HOME_URL=https://example.invalid\n"), 0o644)
	got, err := ParseOSRelease(path)
	if err != nil {
		t.Fatal(err)
	}
	if got["PRETTY_NAME"] != "Sunlight OS 2.2" {
		t.Errorf("PRETTY_NAME = %q", got["PRETTY_NAME"])
	}
	if got["HOME_URL"] != "https://example.invalid" {
		t.Errorf("HOME_URL = %q", got["HOME_URL"])
	}
}

// TestDetectVM_DMIMatches walks the DMI vendor table with fixture
// values to lock in each hypervisor's canonical mapping. Added
// alongside the "Alibaba Cloud ECS" entry (mirrored from systemd
// abffa868a8) — if that regressed to bare "Alibaba Cloud" the test
// would catch it, and the same coverage is useful for the older
// entries as regression insurance.
func TestDetectVM_DMIMatches(t *testing.T) {
	cases := []struct{ vendor, want string }{
		{"QEMU", "qemu"},
		{"KVM", "kvm"},
		{"VMware, Inc.", "vmware"},
		{"innotek GmbH", "oracle"},
		{"Xen", "xen"},
		{"Microsoft Corporation", "microsoft"},
		{"Amazon EC2", "amazon"},
		{"Alibaba Cloud ECS", "kvm"},
	}
	dir := t.TempDir()
	sv := filepath.Join(dir, "sys_vendor")
	defer withPath(&DMISysVendorPath, sv)()
	// Product + bios are unset so only sys_vendor drives the match.
	defer withPath(&DMIProductNamePath, filepath.Join(dir, "missing"))()
	defer withPath(&DMIBiosVendorPath, filepath.Join(dir, "missing"))()
	for _, c := range cases {
		if err := os.WriteFile(sv, []byte(c.vendor+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := detectVM(); got != c.want {
			t.Errorf("detectVM() for %q = %q, want %q", c.vendor, got, c.want)
		}
	}
}

// swapPath sets *p to v and returns a restore function; a small helper
// that keeps test bodies short.
func swapPath(p *string, v string) func() {
	old := *p
	*p = v
	return func() { *p = old }
}

// withPath is a synonym used in TestRunStatus_TextFromFixtures where
// the caller wants to daisy-chain many `defer withPath(...)()` calls.
func withPath(p *string, v string) func() {
	return swapPath(p, v)
}
