package main

import (
	"strings"
	"testing"
)

func TestPickTransientDir(t *testing.T) {
	for _, c := range []struct {
		name     string
		searched []string
		runtime  string
		want     string
	}{
		{"system", []string{"/etc/slinit.d", "/run/slinit.d", "/lib/slinit.d"}, "", "/run/slinit.d"},
		{"user", []string{"/home/u/.config/slinit.d", "/run/user/1000/slinit.d", "/etc/slinit.d/user"},
			"/run/user/1000", "/run/user/1000/slinit.d"},
		// The client's runtime dir differs from the daemon's: not one
		// the daemon reads, so it must not be picked.
		{"user, other runtime dir", []string{"/home/u/.config/slinit.d", "/run/user/1000/slinit.d"},
			"/run/user/2000", ""},
		{"custom --services-dir", []string{"/srv/services"}, "/run/user/1000", ""},
	} {
		got, err := pickTransientDir(c.searched, c.runtime)
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
		if c.want == "" && (err == nil || !strings.Contains(err.Error(), "searches")) {
			t.Errorf("%s: expected an error listing the daemon's dirs, got %v", c.name, err)
		}
	}
}
