package main

import (
	"os"
	"path/filepath"
	"testing"

	etchostname "github.com/sunlightlinux/slinit/pkg/hostname"
)

func TestBootHostnameToSet(t *testing.T) {
	old := etchostname.HostnamePath
	t.Cleanup(func() { etchostname.HostnamePath = old })
	etchostname.HostnamePath = filepath.Join(t.TempDir(), "hostname")

	for _, tc := range []struct{ file, cur, want string }{
		{"# set by the installer\nbox\n", "(none)", "box"},
		{"host.example.com.\n", "localhost", "host.example.com"},
		{"box\n", "box", ""},
		// A container runtime set the UTS name to the file verbatim.
		{"my_host\n", "my_host", ""},
		{"web-??\n", "localhost", ""},
		{"", "localhost", ""},
	} {
		if err := os.WriteFile(etchostname.HostnamePath, []byte(tc.file), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := bootHostnameToSet(tc.cur); got != tc.want {
			t.Errorf("file %q, kernel %q: set %q, want %q", tc.file, tc.cur, got, tc.want)
		}
	}
}
