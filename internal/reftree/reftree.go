// Package reftree finds the upstream source trees some tests check slinit
// against (systemd's), where the checkouts put them. Only tests import it.
package reftree

import (
	"os"
	"path/filepath"
)

// SystemdEnv names a systemd source tree explicitly, overriding the search.
const SystemdEnv = "SLINIT_SYSTEMD_SRC"

// Systemd returns the systemd source tree: $SLINIT_SYSTEMD_SRC, else the
// first of these, relative to this module's root, that holds systemd's
// sources: ../systemd (the tree beside slinit's checkout) or
// ../../sunlight-os/systemd (slinit as src/init of sunlight-repos, with
// the sunlight-os checkout's systemd). ok is false when none does.
func Systemd() (dir string, ok bool) {
	if d := os.Getenv(SystemdEnv); d != "" {
		return d, isSystemd(d)
	}
	root, ok := moduleRoot()
	if !ok {
		return "", false
	}
	for _, d := range []string{
		filepath.Join(root, "..", "systemd"),
		filepath.Join(root, "..", "..", "sunlight-os", "systemd"),
	} {
		if isSystemd(d) {
			return filepath.Clean(d), true
		}
	}
	return "", false
}

func isSystemd(d string) bool {
	_, err := os.Stat(filepath.Join(d, "src", "shared", "seccomp-util.c"))
	return err == nil
}

// moduleRoot walks up from the working directory (a test's package
// directory) to the directory holding go.mod.
func moduleRoot() (string, bool) {
	d, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d, true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false
		}
		d = parent
	}
}
