package main

import (
	"os"
	"strings"
	"testing"
)

// TestResolveServiceDirsUserModeXDGDedup exercises the dinit-parity
// change: when both XDG_CONFIG_HOME and $HOME/.config/slinit.d
// resolve to distinct paths, BOTH must be in the search list —
// previously only one was picked and the other was silently dropped,
// which surprised users with non-default XDG_CONFIG_HOME.
//
// Behaviour matrix:
//   XDG unset             → $HOME/.config/slinit.d only
//   XDG = ~/.config       → $HOME/.config/slinit.d only (dedup)
//   XDG = /custom/xdg     → both /custom/xdg/slinit.d AND $HOME/.config/slinit.d
func TestResolveServiceDirsUserModeXDGDedup(t *testing.T) {
	prev, had := os.LookupEnv("XDG_CONFIG_HOME")
	defer func() {
		if had {
			os.Setenv("XDG_CONFIG_HOME", prev)
		} else {
			os.Unsetenv("XDG_CONFIG_HOME")
		}
	}()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no user home dir on this test host: %v", err)
	}
	defaultUser := home + "/.config/slinit.d"

	t.Run("xdg unset", func(t *testing.T) {
		os.Unsetenv("XDG_CONFIG_HOME")
		dirs := resolveServiceDirs("", false)
		if !contains(dirs, defaultUser) {
			t.Errorf("expected %q in dirs, got %v", defaultUser, dirs)
		}
		if countMatches(dirs, defaultUser) != 1 {
			t.Errorf("expected %q exactly once, got %d in %v",
				defaultUser, countMatches(dirs, defaultUser), dirs)
		}
	})

	t.Run("xdg set to non-default → both present", func(t *testing.T) {
		os.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
		customDir := "/custom/xdg/slinit.d"
		dirs := resolveServiceDirs("", false)
		if !contains(dirs, customDir) {
			t.Errorf("expected %q in dirs, got %v", customDir, dirs)
		}
		if !contains(dirs, defaultUser) {
			t.Errorf("expected %q in dirs too (dinit-parity: not lost when XDG differs), got %v",
				defaultUser, dirs)
		}
	})

	t.Run("xdg set to default → deduped", func(t *testing.T) {
		os.Setenv("XDG_CONFIG_HOME", home+"/.config")
		dirs := resolveServiceDirs("", false)
		if countMatches(dirs, defaultUser) != 1 {
			t.Errorf("expected %q exactly once (dedup), got %d in %v",
				defaultUser, countMatches(dirs, defaultUser), dirs)
		}
	})
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

func countMatches(ss []string, v string) int {
	n := 0
	for _, s := range ss {
		if s == v {
			n++
		}
	}
	return n
}

// TestResolveServiceDirsSystemModeUnchanged — the system-mode list
// wasn't touched by the dinit-parity fixes; this test pins it so a
// future refactor doesn't accidentally drop one of the standard
// dirs.
func TestResolveServiceDirsSystemModeUnchanged(t *testing.T) {
	dirs := resolveServiceDirs("", true)
	for _, want := range []string{"/etc/slinit.d", "/run/slinit.d", "/usr/local/lib/slinit.d", "/lib/slinit.d"} {
		if !contains(dirs, want) {
			t.Errorf("system-mode dirs missing %q: %v", want, dirs)
		}
	}
	// System mode never returns user dirs.
	for _, unwanted := range []string{"/root/.config/slinit.d"} {
		if strings.Contains(strings.Join(dirs, ","), unwanted) {
			t.Errorf("system-mode dirs should not contain %q: %v", unwanted, dirs)
		}
	}
}
