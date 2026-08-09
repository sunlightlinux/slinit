package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseArgs_Defaults(t *testing.T) {
	opts, err := parseArgs(nil)
	if err != nil {
		t.Fatalf("parseArgs(nil): %v", err)
	}
	if opts.cmd != "status" {
		t.Errorf("default cmd = %q, want status", opts.cmd)
	}
	if opts.scope != 0 || opts.jsonMode != "" {
		t.Errorf("unexpected non-zero scope=%d json=%q", opts.scope, opts.jsonMode)
	}
}

func TestParseArgs_Subcommand(t *testing.T) {
	cases := []struct {
		in  []string
		cmd string
		nar int
	}{
		{[]string{"status"}, "status", 0},
		{[]string{"hostname"}, "hostname", 0},
		{[]string{"hostname", "srv-01"}, "hostname", 1},
		{[]string{"icon-name", "computer-laptop"}, "icon-name", 1},
		{[]string{"chassis"}, "chassis", 0},
		{[]string{"deployment", "prod"}, "deployment", 1},
		{[]string{"location", "Bucharest, RO"}, "location", 1},
	}
	for _, c := range cases {
		opts, err := parseArgs(c.in)
		if err != nil {
			t.Fatalf("parseArgs(%v): %v", c.in, err)
		}
		if opts.cmd != c.cmd {
			t.Errorf("parseArgs(%v).cmd = %q, want %q", c.in, opts.cmd, c.cmd)
		}
		if len(opts.args) != c.nar {
			t.Errorf("parseArgs(%v).args len = %d, want %d", c.in, len(opts.args), c.nar)
		}
	}
}

func TestParseArgs_ScopeFlags(t *testing.T) {
	cases := []struct {
		in    []string
		want  scope
		which string
	}{
		{[]string{"--static", "hostname", "n"}, scopeStatic, "static"},
		{[]string{"--transient", "hostname", "n"}, scopeTransient, "transient"},
		{[]string{"--pretty", "hostname", "Foo"}, scopePretty, "pretty"},
		{[]string{"--static", "--pretty", "hostname", "n"}, scopeStatic | scopePretty, "static+pretty"},
	}
	for _, c := range cases {
		opts, err := parseArgs(c.in)
		if err != nil {
			t.Fatalf("[%s] parseArgs: %v", c.which, err)
		}
		if opts.scope != c.want {
			t.Errorf("[%s] scope = %b, want %b", c.which, opts.scope, c.want)
		}
	}
}

func TestParseArgs_JSON(t *testing.T) {
	cases := map[string]string{
		"--json=off":    "off",
		"--json=pretty": "pretty",
		"--json=short":  "short",
	}
	for in, want := range cases {
		opts, err := parseArgs([]string{in, "status"})
		if err != nil {
			t.Fatalf("parseArgs(%s): %v", in, err)
		}
		if opts.jsonMode != want {
			t.Errorf("%s → jsonMode=%q, want %q", in, opts.jsonMode, want)
		}
	}
	if _, err := parseArgs([]string{"--json=bogus"}); err == nil {
		t.Error("--json=bogus should error")
	}
}

func TestParseArgs_UnknownFlag(t *testing.T) {
	if _, err := parseArgs([]string{"--nope"}); err == nil {
		t.Error("expected error for --nope")
	}
}

func TestMachineInfo_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "machine-info")
	restore := swapPath(&machineInfoPath, path)
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
	mi, err := loadMachineInfo()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := mi.get("PRETTY_HOSTNAME"); got != "Ionut's Laptop" {
		t.Errorf("PRETTY_HOSTNAME = %q, want Ionut's Laptop", got)
	}
	if got := mi.get("ICON_NAME"); got != "computer-laptop" {
		t.Errorf("ICON_NAME = %q", got)
	}
	// Overwrite existing key + add a new one.
	mi.set("PRETTY_HOSTNAME", "Ceres VM")
	mi.set("DEPLOYMENT", "production")
	if err := mi.save(); err != nil {
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

func TestMachineInfo_DeleteViaEmptySet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "machine-info")
	restore := swapPath(&machineInfoPath, path)
	defer restore()

	if err := os.WriteFile(path, []byte("CHASSIS=laptop\nICON_NAME=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mi, err := loadMachineInfo()
	if err != nil {
		t.Fatal(err)
	}
	mi.set("CHASSIS", "") // delete
	if err := mi.save(); err != nil {
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

func TestValidateHostname(t *testing.T) {
	ok := []string{"srv-01", "host.example.com", "a", "A1"}
	for _, n := range ok {
		if err := validateHostname(n); err != nil {
			t.Errorf("validateHostname(%q) = %v, want nil", n, err)
		}
	}
	bad := []string{"", "-nope", "nope-", ".nope", "no..pe", "local host", "localhost", strings.Repeat("a", 65)}
	for _, n := range bad {
		if err := validateHostname(n); err == nil {
			t.Errorf("validateHostname(%q) = nil, want error", n)
		}
	}
}

func TestValidChassis(t *testing.T) {
	for _, ok := range []string{"desktop", "laptop", "convertible", "server", "tablet", "vm", "container"} {
		if !validChassis(ok) {
			t.Errorf("validChassis(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "phone", "workstation", "SERVER"} {
		if validChassis(bad) {
			t.Errorf("validChassis(%q) = true", bad)
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
	got, err := parseOSRelease(path)
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

func TestRunStatus_TextFromFixtures(t *testing.T) {
	dir := t.TempDir()
	seed := func(name, contents string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(contents), 0o644)
		return p
	}
	defer withPath(&hostnamePath, seed("hostname", "ceres\n"))()
	defer withPath(&machineInfoPath, seed("machine-info",
		`PRETTY_HOSTNAME="Ceres VM"`+"\nCHASSIS=vm\n"))()
	defer withPath(&machineIDPath, seed("machine-id", "abcdef1234567890abcdef1234567890\n"))()
	defer withPath(&bootIDPath, seed("boot-id", "11111111-2222-3333-4444-555555555555\n"))()
	defer withPath(&osReleasePath, seed("os-release", `PRETTY_NAME="Sunlight OS 2.2"`+"\n"))()
	// Empty DMI dir → chassis auto-detect falls back to "" or VM.
	defer withPath(&dmiChassisTypePath, filepath.Join(dir, "missing"))()
	defer withPath(&dmiSysVendorPath, filepath.Join(dir, "missing"))()
	defer withPath(&dmiProductNamePath, filepath.Join(dir, "missing"))()
	defer withPath(&dmiBiosVendorPath, filepath.Join(dir, "missing"))()
	defer withPath(&dmiBiosVersionPath, filepath.Join(dir, "missing"))()
	defer withPath(&dmiBiosDatePath, filepath.Join(dir, "missing"))()
	defer withPath(&proc1EnvironPath, filepath.Join(dir, "missing"))()
	defer withPath(&proc1CgroupPath, filepath.Join(dir, "missing"))()

	var buf bytes.Buffer
	if err := runStatus(&buf, options{cmd: "status"}); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Static hostname: ceres",
		"Pretty hostname: Ceres VM",
		"Machine ID: abcdef1234567890abcdef1234567890",
		"Boot ID: 11111111-2222-3333-4444-555555555555",
		"Operating System: Sunlight OS 2.2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q, got:\n%s", want, out)
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
