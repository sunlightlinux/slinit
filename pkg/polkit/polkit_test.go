package polkit

import (
	"errors"
	"testing"

	"github.com/godbus/dbus/v5"
)

func checker(uid uint32, uidErr error, ok, challenge bool, checkErr error) (*Checker, *int) {
	asked := 0
	return &Checker{
		UID: func(dbus.Sender) (uint32, error) { return uid, uidErr },
		Check: func(dbus.Sender, string, map[string]string, bool) (bool, bool, error) {
			asked++
			return ok, challenge, checkErr
		},
	}, &asked
}

func TestAuthorize(t *testing.T) {
	const action = "org.freedesktop.timedate1.set-timezone"
	cases := []struct {
		name        string
		c           func() (*Checker, *int)
		interactive bool
		want        string // "" = allowed, else the D-Bus error name
		asksPolkit  bool
	}{
		{"root, polkit not asked", func() (*Checker, *int) { return checker(0, nil, false, false, nil) }, false, "", false},
		{"user allowed by polkit", func() (*Checker, *int) { return checker(1000, nil, true, false, nil) }, false, "", true},
		{"user refused by polkit", func() (*Checker, *int) { return checker(1000, nil, false, false, nil) }, true, "org.freedesktop.DBus.Error.AccessDenied", true},
		{"challenge, interaction not allowed", func() (*Checker, *int) { return checker(1000, nil, false, true, nil) }, false, "org.freedesktop.DBus.Error.InteractiveAuthorizationRequired", true},
		{"polkitd unreachable: fail closed", func() (*Checker, *int) { return checker(1000, nil, true, false, errors.New("no polkit")) }, true, "org.freedesktop.DBus.Error.AccessDenied", true},
		{"caller uid unknown", func() (*Checker, *int) { return checker(0, errors.New("gone"), true, false, nil) }, true, "org.freedesktop.DBus.Error.AccessDenied", false},
	}
	for _, tc := range cases {
		c, asked := tc.c()
		err := c.Authorize(":1.5", action, nil, tc.interactive)
		got := ""
		if err != nil {
			got = err.Name
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
		if (*asked > 0) != tc.asksPolkit {
			t.Errorf("%s: polkit asked=%v, want %v", tc.name, *asked > 0, tc.asksPolkit)
		}
	}
}
