package service

import "testing"

// slinit puts vtty sockets here and `slinitctl attach` looks here, so
// both must agree. A user instance used to be pointed at /run/slinit,
// which it usually cannot write, while attach looked in ~/.slinit.
func TestVTTYSocketDir(t *testing.T) {
	if got := VTTYSocketDir(false); got != "/run/slinit" {
		t.Errorf("system: %q", got)
	}
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/4242")
	if got := VTTYSocketDir(true); got != "/run/user/4242/slinit" {
		t.Errorf("user with XDG_RUNTIME_DIR: %q", got)
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", "/home/u")
	if got := VTTYSocketDir(true); got != "/home/u/.slinit" {
		t.Errorf("user without XDG_RUNTIME_DIR: %q", got)
	}
}
