package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// slinitctl clean removes the kinds asked for and nothing else, and only
// from a stopped service.
func TestCleanService(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	ss := server.services

	base := t.TempDir()
	dir := func(kind string) string { return filepath.Join(base, kind, "svc") }
	var dirs []process.ServiceDir
	for _, k := range []string{"runtime", "state", "cache", "logs", "configuration"} {
		os.MkdirAll(dir(k), 0o755)
		os.WriteFile(filepath.Join(dir(k), "data"), []byte("x"), 0o644)
		dirs = append(dirs, process.ServiceDir{Path: dir(k), Mode: 0o755, Kind: k})
	}
	svc := service.NewProcessService(ss, "svc")
	svc.SetCommand([]string{"/bin/sleep", "60"})
	svc.Record().SetServiceDirs(dirs, 1) // runtime-directory-preserve = yes: the stop keeps it
	ss.AddService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()
	handle := loadHandle(t, conn, "svc")
	clean := func(what uint8) (uint8, string) {
		t.Helper()
		if err := WritePacket(conn, CmdCleanService, append(EncodeHandle(handle), what)); err != nil {
			t.Fatal(err)
		}
		rply, body := readReply(t, conn)
		return rply, string(body)
	}
	exists := func(k string) bool { _, err := os.Stat(dir(k)); return err == nil }

	ss.StartService(svc)
	deadline := time.Now().Add(3 * time.Second)
	for svc.State() != service.StateStarted && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if rply, body := clean(CleanAll); rply != RplyNAK || !strings.Contains(body, "stop it first") {
		t.Errorf("clean of a started service: reply %d %q, want NAK", rply, body)
	}
	if !exists("state") || !exists("runtime") {
		t.Fatal("a refused clean removed something")
	}
	ss.StopService(svc)
	for svc.State() != service.StateStopped && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	rply, body := clean(CleanDefault)
	if rply != RplyCleanResult {
		t.Fatalf("reply %d %q", rply, body)
	}
	for k, want := range map[string]bool{"runtime": false, "cache": false, "state": true, "logs": true, "configuration": true} {
		if exists(k) != want {
			t.Errorf("default clean: %s exists=%v, want %v", k, exists(k), want)
		}
	}
	if !strings.Contains(body, dir("runtime")) || !strings.Contains(body, dir("cache")) || strings.Contains(body, dir("state")) {
		t.Errorf("reported %q", body)
	}

	if rply, body := clean(CleanState | CleanLogs); rply != RplyCleanResult || exists("state") || exists("logs") || !exists("configuration") {
		t.Errorf("state,logs: reply %d %q; configuration must stay", rply, body)
	}
	if rply, body := clean(CleanAll); rply != RplyCleanResult || exists("configuration") || body != dir("configuration") {
		t.Errorf("all: reply %d %q", rply, body)
	}
	if rply, body := clean(CleanAll); rply != RplyCleanResult || body != "" {
		t.Errorf("nothing left: reply %d %q, want an empty result", rply, body)
	}
	if rply, _ := clean(0); rply != RplyBadReq {
		t.Errorf("empty mask: reply %d, want BadReq", rply)
	}
	if rply, _ := clean(0x80); rply != RplyBadReq {
		t.Errorf("unknown bit: reply %d, want BadReq", rply)
	}
}

// --what=fdstore closes what the store keeps (file-descriptor-store-
// preserve keeps it past a stop); the default includes it.
func TestCleanServiceFDStore(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	ss := server.services
	svc := service.NewProcessService(ss, "fds")
	svc.SetCommand([]string{"/bin/true"})
	svc.Record().SetFDStoreMax(4)
	ss.AddService(svc)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := svc.Record().FDStore().Add(process.FDStoreEntry{Name: "p", File: r}); err != nil {
		t.Fatal(err)
	}
	conn := connectTest(t, sockPath)
	defer conn.Close()
	handle := loadHandle(t, conn, "fds")
	if err := WritePacket(conn, CmdCleanService, append(EncodeHandle(handle), CleanState)); err != nil {
		t.Fatal(err)
	}
	if rply, body := readReply(t, conn); rply != RplyCleanResult || len(body) != 0 || svc.Record().FDStore().Len() != 1 {
		t.Fatalf("--what=state touched the fd store: %d %q", rply, body)
	}
	if err := WritePacket(conn, CmdCleanService, append(EncodeHandle(handle), CleanDefault)); err != nil {
		t.Fatal(err)
	}
	if rply, body := readReply(t, conn); rply != RplyCleanResult || string(body) != "fdstore" || svc.Record().FDStore().Len() != 0 {
		t.Fatalf("default clean: %d %q, store holds %d", rply, body, svc.Record().FDStore().Len())
	}
}
