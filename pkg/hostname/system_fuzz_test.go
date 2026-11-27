package hostname

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzReadStatic: whatever /etc/hostname holds, the static hostname PID 1
// hands to sethostname is either none or a valid name. Until 0667d6f PID
// 1 gave the kernel the whole file trimmed, a comment above the name
// included, and hostnamed refused "host.example.com." outright.
//
// It also pins Cleanup, systemd's hostname_cleanup, to three properties:
// cleaning twice changes nothing more; a cleaned name without the
// wildcard characters it keeps ('?', '$') is valid or empty; and a name
// that is already valid comes back unchanged, so a correct /etc/hostname
// is never rewritten.
func FuzzReadStatic(f *testing.F) {
	for _, s := range []string{
		"ceres\n",
		"# set by the installer\n\nnode.example.com.\n",
		"my_host\n",
		"-a..b--c.\n",
		"  \t\n#only a comment\n",
		strings.Repeat("a", 70) + "\n",
		strings.Repeat("ab.", 30) + "\n",
		"h?st$\n",
		"\xff\xfe\n",
	} {
		f.Add(s)
	}
	dir := f.TempDir()
	path := filepath.Join(dir, "hostname")
	saved := HostnamePath
	HostnamePath = path
	f.Cleanup(func() { HostnamePath = saved })

	f.Fuzz(func(t *testing.T, content string) {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := ReadStatic(); got != "" && !Valid(got) {
			t.Fatalf("ReadStatic() of %q = %q, which is not a valid hostname", content, got)
		}

		raw := ReadStaticRaw()
		c := Cleanup(raw)
		if cc := Cleanup(c); cc != c {
			t.Fatalf("Cleanup is not idempotent on %q: %q then %q", raw, c, cc)
		}
		if c != "" && !strings.ContainsAny(c, "?$") && !Valid(c) {
			t.Fatalf("Cleanup(%q) = %q, not valid", raw, c)
		}
		if Valid(raw) && c != raw {
			t.Fatalf("Cleanup changed the valid name %q to %q", raw, c)
		}
	})
}
