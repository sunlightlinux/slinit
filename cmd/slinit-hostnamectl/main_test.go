package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/hostname"
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

func TestTransientDisplay(t *testing.T) {
	cases := []struct {
		kernel, static, want string
		note                 string
	}{
		{"srv-01", "srv-01", "", "matches static → hide"},
		{"", "srv-01", "", "kernel empty → hide"},
		{"(none)", "localhost", "", "Linux placeholder → hide"},
		{"(none)", "srv-01", "", "placeholder even against real static"},
		{"localhost", "", "", "distro default → hide"},
		{"localhost.localdomain", "srv-01", "", "distro-fqdn default → hide"},
		{"srv-02", "srv-01", "srv-02", "real difference → show"},
		{"box", "", "box", "static missing, kernel real → show"},
	}
	for _, c := range cases {
		got := transientDisplay(c.kernel, c.static)
		if got != c.want {
			t.Errorf("[%s] transientDisplay(%q,%q) = %q, want %q",
				c.note, c.kernel, c.static, got, c.want)
		}
	}
}

func TestRunStatus_TextFromFixtures(t *testing.T) {
	dir := t.TempDir()
	seed := func(name, contents string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(contents), 0o644)
		return p
	}
	defer withPath(&hostname.HostnamePath, seed("hostname", "ceres\n"))()
	defer withPath(&hostname.MachineInfoPath, seed("machine-info",
		`PRETTY_HOSTNAME="Ceres VM"`+"\nCHASSIS=vm\n"))()
	defer withPath(&hostname.MachineIDPath, seed("machine-id", "abcdef1234567890abcdef1234567890\n"))()
	defer withPath(&hostname.BootIDPath, seed("boot-id", "11111111-2222-3333-4444-555555555555\n"))()
	defer withPath(&hostname.OSReleasePath, seed("os-release", `PRETTY_NAME="Sunlight OS 2.2"`+"\n"))()
	// Empty DMI dir → chassis auto-detect falls back to "" or VM.
	defer withPath(&hostname.DMIChassisTypePath, filepath.Join(dir, "missing"))()
	defer withPath(&hostname.DMISysVendorPath, filepath.Join(dir, "missing"))()
	defer withPath(&hostname.DMIProductNamePath, filepath.Join(dir, "missing"))()
	defer withPath(&hostname.DMIBiosVendorPath, filepath.Join(dir, "missing"))()
	defer withPath(&hostname.DMIBiosVersionPath, filepath.Join(dir, "missing"))()
	defer withPath(&hostname.DMIBiosDatePath, filepath.Join(dir, "missing"))()
	defer withPath(&hostname.Proc1EnvironPath, filepath.Join(dir, "missing"))()
	defer withPath(&hostname.Proc1CgroupPath, filepath.Join(dir, "missing"))()

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
