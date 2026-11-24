package config

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// chroot was applied only to type = process and silently ignored for
// every other type: a scripted service with chroot = /nonexistent-jail
// loaded, started, and ran its command outside the jail. Before the fix
// all the chroot services below loaded. lock-file is ignored there too,
// but confines nothing, so it still loads (with a warning) — a config
// that loads on 4.0.2 must keep loading unless keeping it is unsafe.
func TestChrootRejectedForNonProcessTypes(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ svc, body, want string }{
		{"scr", "type = scripted\nchroot = /nonexistent-jail\ncommand = /bin/true\n", "chroot"},
		{"bg", "type = bgprocess\nchroot = /jail\ncommand = /bin/true\npid-file = /run/bg.pid\n", "chroot"},
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

	writeServiceFile(t, dir, "lk", "type = scripted\nlock-file = /run/x.lock\ncommand = /bin/true\n")
	ss := service.NewServiceSet(&testReloadLogger{})
	l := NewDirLoader(ss, []string{dir})
	ss.SetLoader(l)
	if _, err := l.LoadService("lk"); err != nil {
		t.Errorf("lock-file on a scripted service no longer loads: %v", err)
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
