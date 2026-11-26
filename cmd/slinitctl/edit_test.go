package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// editTestDaemon runs the real control server over a fresh ServiceSet,
// so the test holds slinitctl to what the daemon actually replies.
func editTestDaemon(t *testing.T) (*service.ServiceSet, net.Conn) {
	t.Helper()
	logger := logging.New(logging.LevelError)
	set := service.NewServiceSet(logger)
	sock := filepath.Join(t.TempDir(), "ctl.sock")
	srv := control.NewServer(set, sock, logger)
	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); srv.Stop() })
	var conn net.Conn
	var err error
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if conn, err = net.Dial("unix", sock); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return set, conn
}

func addEditTestService(set *service.ServiceSet, name, dir string) {
	svc := service.NewInternalService(set, name)
	svc.Record().SetServiceDir(dir)
	set.AddService(svc)
}

// `slinitctl edit` asked for the loader's search path and expected a
// reply the daemon never sends, so it failed for every service. It
// resolves the file the loader read: the full name, a template's base
// name, or a systemd unit's NAME.service.
func TestEditResolvesTheLoadedDescription(t *testing.T) {
	set, conn := editTestDaemon(t)
	dir := t.TempDir()
	for _, f := range []string{"web", "getty", "legacy.service"} {
		os.WriteFile(filepath.Join(dir, f), []byte("type = internal\n"), 0o644)
	}
	cases := map[string]string{
		"web":        "web",
		"getty@tty1": "getty",
		"legacy":     "legacy.service",
	}
	for name, file := range cases {
		addEditTestService(set, name, dir)
		got, err := editDescriptionPath(conn, name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if want := filepath.Join(dir, file); got != want {
			t.Errorf("%s: path %q, want %q", name, got, want)
		}
	}
	addEditTestService(set, "nodir", "")
	if _, err := editDescriptionPath(conn, "nodir"); err == nil || !strings.Contains(err.Error(), "no description file") {
		t.Errorf("service with no on-disk description: err %v", err)
	}
}
