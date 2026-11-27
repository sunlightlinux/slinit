package hostname

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The vectors are systemd's own, from test-hostname-util.c, for
// hostname_is_valid with no flags — the rule Valid claims to follow.
func TestValid_SystemdVectors(t *testing.T) {
	cases := map[string]bool{
		"foobar":                 true,
		"foobar.com":             true,
		"foobar.com.":            false,
		"fooBAR":                 true,
		"fooBAR.com":             true,
		"fooBAR.":                false,
		"fooBAR.com.":            false,
		"fööbar":                 false,
		"":                       false,
		".":                      false,
		"..":                     false,
		"foobar.":                false,
		".foobar":                false,
		"foo..bar":               false,
		"foo.bar..":              false,
		strings.Repeat("x", 104): false,
		"au-xph5-rvgrdsb5hcxc-47et3a5vvkrc-server-wyoz4elpdpe3.openstack.local": false,
		"foo??bar": false,
		// Hyphen rules: not at the start or end of a label.
		"-foo":     false,
		"foo-":     false,
		"foo-.bar": false,
		"foo.-bar": false,
		"foo-bar":  true,
		"a--b":     true,
		// The 64-byte limit is inclusive.
		strings.Repeat("a", 64): true,
		strings.Repeat("a", 65): false,
		".host":                 false,
		"foo bar":               false,
		"foo_bar":               false,
	}
	for s, want := range cases {
		if got := Valid(s); got != want {
			t.Errorf("Valid(%q) = %v, want %v", s, got, want)
		}
	}
}

// SafeString as hostnamed uses it: non-strict for the pretty hostname and
// the location (systemd allows backslashes, quotes and globs there),
// strict for the icon name, which may become a file name.
func TestSafeString(t *testing.T) {
	cases := []struct {
		s             string
		loose, strict bool
	}{
		{"hello", true, true},
		{"hello world", true, true},
		{"Lennart's Laptop", true, false},
		{`back\slash`, true, false},
		{"glob*", true, false},
		{"a/b", true, false},
		{".", true, false},
		{"..", true, false},
		{"...", true, true},
		{"", false, false},
		{"new\nline", false, false},
		{"tab\there", false, false},
		{"del\x7f", false, false},
		{"bad\xffutf8", false, false},
		{"ünïcode ✓", true, true},
	}
	for _, c := range cases {
		if got := SafeString(c.s, false); got != c.loose {
			t.Errorf("SafeString(%q, false) = %v, want %v", c.s, got, c.loose)
		}
		if got := SafeString(c.s, true); got != c.strict {
			t.Errorf("SafeString(%q, true) = %v, want %v", c.s, got, c.strict)
		}
	}
}

func TestValidDeployment(t *testing.T) {
	for s, want := range map[string]bool{
		"production": true, "eu-west.1:a": true, "": false,
		"with space": false, "slash/x": false, "ünï": false,
	} {
		if got := ValidDeployment(s); got != want {
			t.Errorf("ValidDeployment(%q) = %v, want %v", s, got, want)
		}
	}
}

// /etc/hostname: the first line that is neither blank nor a comment;
// writing an empty name removes the file; a missing file reads as "".
func TestStaticHostnameFile(t *testing.T) {
	old := HostnamePath
	t.Cleanup(func() { HostnamePath = old })
	HostnamePath = filepath.Join(t.TempDir(), "etc", "hostname")

	if got := ReadStatic(); got != "" {
		t.Fatalf("missing file read as %q", got)
	}
	if err := os.MkdirAll(filepath.Dir(HostnamePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(HostnamePath, []byte("# comment\n\n  myhost  \nsecond\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadStatic(); got != "myhost" {
		t.Fatalf("ReadStatic = %q, want myhost", got)
	}

	if err := WriteStatic("newhost"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(HostnamePath)
	if string(b) != "newhost\n" {
		t.Fatalf("file holds %q", b)
	}
	st, _ := os.Stat(HostnamePath)
	if st.Mode().Perm() != 0o644 {
		t.Errorf("mode %v, want 0644", st.Mode().Perm())
	}
	// No temporary file left beside it.
	ents, _ := os.ReadDir(filepath.Dir(HostnamePath))
	if len(ents) != 1 {
		t.Errorf("directory holds %d entries after the write", len(ents))
	}

	if err := WriteStatic(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(HostnamePath); !os.IsNotExist(err) {
		t.Fatalf("empty name did not remove the file: %v", err)
	}
	if err := WriteStatic(""); err != nil {
		t.Errorf("removing an already missing file: %v", err)
	}
}

// The files systemd reads keep its grammar: "export KEY=" is not an
// assignment of KEY there, only in a shell-sourced file.
func TestExportOnlyInShellEnvFiles(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	os.WriteFile(p, []byte("export KEYMAP=us\n"), 0o644)
	plain, err := LoadEnvFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if v := plain.Get("KEYMAP"); v != "" {
		t.Errorf("plain env file read export KEYMAP as KEYMAP=%q", v)
	}
	sh, err := LoadShellEnvFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if v := sh.Get("KEYMAP"); v != "us" {
		t.Errorf("shell env file: KEYMAP=%q, want us", v)
	}
}

// /etc/hostname is normalised as systemd's read_etc_hostname does it: a
// trailing dot (an FQDN written the DNS way) and stray separators are
// dropped, and a name that is still invalid reads as none rather than
// being handed to the kernel.
func TestReadStaticNormalises(t *testing.T) {
	old := HostnamePath
	t.Cleanup(func() { HostnamePath = old })
	HostnamePath = filepath.Join(t.TempDir(), "hostname")

	long := strings.Repeat("a", 70)
	for in, want := range map[string]string{
		"host.example.com.\n": "host.example.com",
		"-foo-\n":             "foo",
		"a..b\n":              "a.b",
		"my_host\n":           "myhost",
		".-\n":                "",
		"web-??\n":            "",
		long + "\n":           long[:64],
		"# c\n  box.lan.  \n": "box.lan",
	} {
		if err := os.WriteFile(HostnamePath, []byte(in), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := ReadStatic(); got != want {
			t.Errorf("ReadStatic(%q) = %q, want %q", in, got, want)
		}
	}
}
