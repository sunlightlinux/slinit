package main

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestRWRemountFlags(t *testing.T) {
	got := rwRemountFlags(unix.ST_RDONLY | unix.ST_NOSUID | unix.ST_NOEXEC | unix.ST_RELATIME)
	if got&unix.MS_RDONLY != 0 {
		t.Error("still read-only")
	}
	if got&unix.MS_REMOUNT == 0 || got&unix.MS_BIND == 0 {
		t.Error("not a bind remount")
	}
	for _, f := range []uintptr{unix.MS_NOSUID, unix.MS_NOEXEC, unix.MS_RELATIME} {
		if got&f == 0 {
			t.Errorf("dropped %#x", f)
		}
	}
	if got&unix.MS_NODEV != 0 {
		t.Error("added nodev")
	}
}
