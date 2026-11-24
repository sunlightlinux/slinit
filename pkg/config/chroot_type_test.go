package config

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// chroot and lock-file were applied only to type = process and silently
// ignored for every other type: a scripted service with
// chroot = /nonexistent-jail loaded, started, and ran its command
// outside the jail. Before the fix both services below loaded.
func TestChrootAndLockFileRejectedForNonProcessTypes(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ svc, body, want string }{
		{"scr", "type = scripted\nchroot = /nonexistent-jail\ncommand = /bin/true\n", "chroot"},
		{"bg", "type = bgprocess\nchroot = /jail\ncommand = /bin/true\npid-file = /run/bg.pid\n", "chroot"},
		{"lk", "type = scripted\nlock-file = /run/x.lock\ncommand = /bin/true\n", "lock-file"},
		{"int", "type = internal\nchroot = /jail\n", "chroot"},
	} {
		writeServiceFile(t, dir, tc.svc, tc.body)
		ss := service.NewServiceSet(&testReloadLogger{})
		l := NewDirLoader(ss, []string{dir})
		ss.SetLoader(l)
		_, err := l.LoadService(tc.svc)
		if err == nil || !strings.Contains(err.Error(), tc.want+" is only supported for type = process") {
			t.Errorf("%s: want a %s rejection, got %v", tc.svc, tc.want, err)
		}
	}
}

// type = process keeps both.
func TestChrootAndLockFileStillAcceptedForProcess(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "p", "type = process\nchroot = /jail\nlock-file = /run/p.lock\ncommand = /bin/true\n")
	ss := service.NewServiceSet(&testReloadLogger{})
	l := NewDirLoader(ss, []string{dir})
	ss.SetLoader(l)
	if _, err := l.LoadService("p"); err != nil {
		t.Fatal(err)
	}
}
