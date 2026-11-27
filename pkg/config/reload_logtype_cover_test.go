package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// Reloading a stopped service whose file no longer names any log
// output must leave it with none, as loading the same file fresh would.
func TestReloadStoppedDropsLogType(t *testing.T) {
	for _, tc := range []struct {
		name, setting string
	}{
		{"logfile", "logfile = /tmp/slinit-reload-logtype-test.log\n"},
		{"buffer", "log-type = buffer\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			ss := service.NewServiceSet(&testReloadLogger{})
			loader := NewDirLoader(ss, []string{dir})
			ss.SetLoader(loader)

			writeServiceFile(t, dir, "svc", "type = process\ncommand = /bin/true\n"+tc.setting)
			svc, err := loader.LoadService("svc")
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			ps := svc.(*service.ProcessService)
			if ps.GetLogType() == service.LogNone {
				t.Fatalf("%q did not set a log type", tc.setting)
			}

			writeServiceFile(t, dir, "svc", "type = process\ncommand = /bin/true\n")
			if _, err := loader.ReloadService(svc); err != nil {
				t.Fatalf("reload: %v", err)
			}
			if got := ps.GetLogType(); got != service.LogNone {
				t.Errorf("log type after reload = %v, want none (the file no longer sets one)", got)
			}
		})
	}
}

// The same for stderr-logfile: dropped from the file, stderr goes back
// to the logfile with stdout.
func TestReloadStoppedDropsStderrLogfile(t *testing.T) {
	dir := t.TempDir()
	out, errLog := filepath.Join(dir, "out.log"), filepath.Join(dir, "err.log")
	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	base := "type = process\ncommand = /bin/sh -c \"echo to-stderr >&2; exec sleep 30\"\nlogfile = " + out + "\n"
	writeServiceFile(t, dir, "svc", base+"stderr-logfile = "+errLog+"\n")
	svc, err := loader.LoadService("svc")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	writeServiceFile(t, dir, "svc", base)
	if _, err := loader.ReloadService(svc); err != nil {
		t.Fatalf("reload: %v", err)
	}
	ss.StartService(svc)
	defer ss.StopService(svc)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, _ := os.ReadFile(out); strings.Contains(string(b), "to-stderr") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	b, _ := os.ReadFile(errLog)
	t.Fatalf("stderr did not reach the logfile after stderr-logfile was removed (stderr-logfile has %q)", b)
}
