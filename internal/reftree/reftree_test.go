package reftree

import (
	"os"
	"path/filepath"
	"testing"
)

// Both checkout layouts are found from a package directory, and the
// environment variable wins over them.
func TestSystemdLayouts(t *testing.T) {
	mk := func(p string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(SystemdEnv, "")
	for _, tc := range []struct{ name, module, systemd string }{
		{"beside", "slinit", "systemd"},
		{"sunlight-repos", "src/init", "sunlight-os/systemd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			mk(filepath.Join(base, tc.module, "go.mod"))
			pkg := filepath.Join(base, tc.module, "pkg", "x")
			if err := os.MkdirAll(pkg, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Chdir(pkg)
			if _, ok := Systemd(); ok {
				t.Fatal("found a systemd tree before there was one")
			}
			mk(filepath.Join(base, tc.systemd, "src", "shared", "seccomp-util.c"))
			got, ok := Systemd()
			if want := filepath.Join(base, tc.systemd); !ok || got != want {
				t.Errorf("Systemd() = %q, %v; want %q", got, ok, want)
			}

			other := t.TempDir()
			t.Setenv(SystemdEnv, other)
			if got, ok := Systemd(); ok || got != other {
				t.Errorf("with %s at a tree that is not systemd: %q, %v", SystemdEnv, got, ok)
			}
			mk(filepath.Join(other, "src", "shared", "seccomp-util.c"))
			if got, ok := Systemd(); !ok || got != other {
				t.Errorf("with %s: %q, %v; want %q", SystemdEnv, got, ok, other)
			}
		})
	}
}
