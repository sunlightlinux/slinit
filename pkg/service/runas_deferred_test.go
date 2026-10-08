package service

import (
	"os"
	"os/user"
	"path/filepath"
	"testing"
	"time"
)

// A run-as user that does not exist used to be dropped with a warning,
// and the service ran with slinit's credentials — root, for PID 1. An
// unresolved user is now looked up again at start, and the start is
// refused if it is still missing.
func TestRunAsUnresolvedRefusesStart(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	set, _ := newTestSet()
	svc := NewProcessService(set, "runas-missing")
	svc.SetCommand([]string{"/bin/sh", "-c", "touch " + marker + "; sleep 60"})
	svc.Record().SetRunAsSpec("nosuchuser-slinit-test")
	set.AddService(svc)

	set.StartService(svc)
	if got := waitState(t, svc, StateStopped, 3*time.Second); got != StateStopped {
		t.Fatalf("state = %v, want STOPPED (start refused)", got)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the command ran although its run-as user does not exist")
	}
}

// A user created after the service was loaded (by slinit-sysusers early
// in boot, say) is picked up when the service starts.
func TestRunAsDeferredResolvesAtStart(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("dropping to another user needs root")
	}
	nobody, err := user.Lookup("nobody")
	if err != nil {
		t.Skip("no nobody user")
	}
	// t.TempDir sits under a 0700 directory nobody cannot enter.
	dir, err := os.MkdirTemp("", "slinit-runas-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "uid")
	set, _ := newTestSet()
	svc := NewProcessService(set, "runas-late")
	svc.SetCommand([]string{"/bin/sh", "-c", "id -u > " + out + "; sleep 60"})
	svc.Record().SetRunAsSpec("nobody")
	set.AddService(svc)
	defer func() {
		set.StopService(svc)
		waitState(t, svc, StateStopped, 5*time.Second)
	}()

	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 3*time.Second); got != StateStarted {
		t.Fatalf("state = %v, want STARTED", got)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		data, err := os.ReadFile(out)
		if err == nil && len(data) > 0 {
			if got := string(data); got != nobody.Uid+"\n" {
				t.Errorf("ran as uid %q, want %s", got, nobody.Uid)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("service did not write its uid")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
