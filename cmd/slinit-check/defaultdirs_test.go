package main

import (
	"reflect"
	"testing"
)

// The linter must look where the daemon looks (cmd/slinit
// resolveServiceDirs), or it checks a file slinit will not run and
// misses ones it will.
func TestDefaultDirsMatchTheDaemon(t *testing.T) {
	want := []string{"/etc/slinit.d", "/run/slinit.d", "/usr/local/lib/slinit.d", "/lib/slinit.d"}
	if got := defaultSystemDirs(); !reflect.DeepEqual(got, want) {
		t.Errorf("system dirs = %v, want %v", got, want)
	}

	t.Setenv("HOME", "/home/u")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	want = []string{"/xdg/slinit.d", "/home/u/.config/slinit.d", "/run/user/1000/slinit.d",
		"/etc/slinit.d/user", "/usr/lib/slinit.d/user", "/usr/local/lib/slinit.d/user"}
	if got := defaultUserDirs(); !reflect.DeepEqual(got, want) {
		t.Errorf("user dirs = %v, want %v", got, want)
	}
}
