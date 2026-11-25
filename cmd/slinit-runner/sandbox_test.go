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

func TestParseMountTree(t *testing.T) {
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
	got := parseMountTree(mi, "/srv")
	want := []mountPoint{
		{point: "/srv", flags: unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC | unix.MS_RELATIME},
		{point: "/srv/a b", flags: unix.MS_NOATIME},
		{point: "/srv/auto", ro: true, flags: unix.MS_NOSUID | unix.MS_STRICTATIME},
		{point: "/srv/over", ro: true, flags: unix.MS_STRICTATIME},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseMountTree(/srv) =\n%+v\nwant\n%+v", got, want)
	}
	if n := len(parseMountTree(mi, "/")); n != 6 {
		t.Errorf("parseMountTree(/) found %d mounts; want 6", n)
	}
	if got := parseMountTree(mi, "/nowhere"); len(got) != 0 {
		t.Errorf("parseMountTree(/nowhere) = %+v; want nothing", got)
	}
}

func TestParseMountOpts(t *testing.T) {
	ro, flags := parseMountOpts("ro,nosuid,nodev,noexec,nodiratime,relatime,nosymfollow")
	want := uintptr(unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC |
		unix.MS_NODIRATIME | unix.MS_RELATIME | unix.MS_NOSYMFOLLOW)
	if !ro || flags != want {
		t.Errorf("got ro=%v flags=%#x; want ro=true flags=%#x", ro, flags, want)
	}
	if ro, _ := parseMountOpts("rw,relatime"); ro {
		t.Error("rw read as ro")
	}
}
