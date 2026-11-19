package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func loadOne(t *testing.T, content string) (*DirLoader, *service.ServiceSet, service.Service, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "s"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ss := service.NewServiceSet(&testReloadLogger{})
	l := NewDirLoader(ss, []string{dir})
	ss.SetLoader(l)
	svc, err := l.LoadService("s")
	if err != nil {
		t.Fatal(err)
	}
	return l, ss, svc, dir
}

func field(svc service.Service, name string) reflect.Value {
	v := reflect.ValueOf(svc).Elem().FieldByName(name)
	if !v.IsValid() {
		panic("no field " + name)
	}
	return v
}

// slinit-service(5) says start-timeout = 0 and stop-timeout = 0 wait
// forever, and restart-delay = 0 restarts at once. The loader applied
// each only when > 0, so an explicit 0 left the default (60s, 10s,
// 0.2s) in place.
func TestExplicitZeroTimingIsHonoured(t *testing.T) {
	for _, c := range []struct{ typ, extra string }{
		{"process", "restart-delay = 0\n"},
		{"bgprocess", "pid-file = /run/s.pid\nrestart-delay = 0\n"},
		{"scripted", ""},
	} {
		_, _, svc, _ := loadOne(t, "type = "+c.typ+"\ncommand = /bin/true\nstart-timeout = 0\nstop-timeout = 0\n"+c.extra)
		for _, f := range []string{"startTimeout", "stopTimeout"} {
			if got := time.Duration(field(svc, f).Int()); got != 0 {
				t.Errorf("%s: %s = %v, want 0 (wait forever)", c.typ, f, got)
			}
		}
		if c.extra != "" {
			if got := time.Duration(field(svc, "restartDelay").Int()); got != 0 {
				t.Errorf("%s: restartDelay = %v, want 0", c.typ, got)
			}
		}
	}
	// Absent still means the documented defaults.
	_, _, svc, _ := loadOne(t, "type = process\ncommand = /bin/true\n")
	if got := time.Duration(field(svc, "startTimeout").Int()); got != service.DefaultStartTimeout {
		t.Errorf("default startTimeout = %v", got)
	}
	if got := field(svc, "maxRestartCount").Int(); got != service.DefaultMaxRestarts {
		t.Errorf("default maxRestartCount = %v", got)
	}
}

// restart-limit-interval alone used to set the count to 0, which means
// unlimited restarts, instead of keeping the documented 3.
func TestRestartLimitIntervalAloneKeepsDefaultCount(t *testing.T) {
	_, _, svc, _ := loadOne(t, "type = process\ncommand = /bin/true\nrestart-limit-interval = 30\n")
	if got := field(svc, "maxRestartCount").Int(); got != service.DefaultMaxRestarts {
		t.Errorf("maxRestartCount = %d, want the default %d", got, service.DefaultMaxRestarts)
	}
	if got := time.Duration(field(svc, "restartInterval").Int()); got != 30*time.Second {
		t.Errorf("restartInterval = %v", got)
	}
}

// These were applied only by a reload (createService had its own copy of
// the setters, without them), so on a first load they did nothing.
func TestFirstLoadAppliesEverySetting(t *testing.T) {
	_, _, svc, _ := loadOne(t, `type = process
command = /bin/true
timeout-abort-sec = 7
timeout-start-failure-mode = abort
exit-type = cgroup
restart-randomized-delay = 3s
restart-max-delay = 9
`)
	if got := time.Duration(field(svc, "timeoutAbortSec").Int()); got != 7*time.Second {
		t.Errorf("timeout-abort-sec: %v", got)
	}
	if got := time.Duration(field(svc, "restartRandomizedDelay").Int()); got != 3*time.Second {
		t.Errorf("restart-randomized-delay: %v", got)
	}
	if got := time.Duration(field(svc, "restartMaxDelay").Int()); got != 9*time.Second {
		t.Errorf("restart-max-delay: %v", got)
	}
	if field(svc, "exitType").IsZero() {
		t.Error("exit-type = cgroup not applied")
	}
	if field(svc, "timeoutStartFailureMode").IsZero() {
		t.Error("timeout-start-failure-mode = abort not applied")
	}
}

// The set finds services by their provides alias too; a reload that
// changes or drops it must move or remove the alias.
func TestReloadMovesProvidesAlias(t *testing.T) {
	l, ss, svc, dir := loadOne(t, "type = process\ncommand = /bin/true\nprovides = old-name\n")
	if ss.FindService("old-name", false) != svc {
		t.Fatal("alias not registered on load")
	}
	os.WriteFile(filepath.Join(dir, "s"), []byte("type = process\ncommand = /bin/true\nprovides = new-name\n"), 0o644)
	if _, err := l.ReloadService(svc); err != nil {
		t.Fatal(err)
	}
	if ss.FindService("new-name", false) != svc {
		t.Error("new alias not registered on reload")
	}
	if ss.FindService("old-name", false) != nil {
		t.Error("old alias still resolves after reload")
	}
	os.WriteFile(filepath.Join(dir, "s"), []byte("type = process\ncommand = /bin/true\n"), 0o644)
	if _, err := l.ReloadService(svc); err != nil {
		t.Fatal(err)
	}
	if ss.FindService("new-name", false) != nil {
		t.Error("dropped alias still resolves after reload")
	}
}
