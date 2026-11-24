package main

import (
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
)

// TestStringListAppend verifies the flag.Value adapter accumulates
// repeated --read-only-path / --read-write-path occurrences in order.
func TestStringListAppend(t *testing.T) {
	var sl stringList
	for _, v := range []string{"/a", "/b", "/c"} {
		if err := sl.Set(v); err != nil {
			t.Fatalf("Set(%q): %v", v, err)
		}
	}
	if !reflect.DeepEqual([]string(sl), []string{"/a", "/b", "/c"}) {
		t.Errorf("got %v, want [/a /b /c]", sl)
	}
	if got, want := sl.String(), "/a,/b,/c"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

// TestStringListRejectsEmpty makes sure an empty value (e.g. from a
// stray "--read-only-path=" on the command line) is rejected rather
// than silently appended.
func TestStringListRejectsEmpty(t *testing.T) {
	var sl stringList
	if err := sl.Set(""); err == nil {
		t.Error("expected error on empty value")
	}
}

func TestParseSubmounts(t *testing.T) {
	const mi = `22 1 0:20 / / rw,relatime - ext4 /dev/sda1 rw
30 22 0:30 / /srv rw,nosuid,nodev,noexec,relatime - tmpfs tmpfs rw
31 30 0:31 / /srv/a\040b rw,noatime - tmpfs tmpfs rw
32 30 0:32 / /srv/auto rw,relatime - autofs systemd-1 rw
33 32 0:33 / /srv/auto ro,nosuid - nfs host:/x rw
34 30 0:34 / /srv/over rw,relatime shared:5 - tmpfs tmpfs rw
35 34 0:35 / /srv/over ro - tmpfs tmpfs rw
36 30 0:36 / /srv/trig rw,relatime - autofs systemd-1 rw
37 22 0:37 / /srvx rw - tmpfs tmpfs rw
`
	got := parseSubmounts(mi, "/srv")
	want := []mountPoint{
		{point: "/srv/a b", flags: unix.MS_NOATIME},
		{point: "/srv/auto", ro: true, flags: unix.MS_NOSUID | unix.MS_STRICTATIME},
		{point: "/srv/over", ro: true, flags: unix.MS_STRICTATIME},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseSubmounts(/srv) =\n%+v\nwant\n%+v", got, want)
	}
	if n := len(parseSubmounts(mi, "/")); n != 5 {
		t.Errorf("parseSubmounts(/) found %d mounts; want 5", n)
	}
}
