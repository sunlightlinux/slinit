package process

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// LISTEN_PID was never exported (a comment promised it "after
// cmd.Start()", which cannot change the child's environment), so
// sd_listen_fds() in a socket-activated service saw LISTEN_FDS without
// LISTEN_PID and ignored the socket. The runner now exports its own pid,
// which the service keeps across exec.
func TestSocketActivatedServiceGetsListenPID(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not in PATH")
	}
	dir := t.TempDir()
	runner := filepath.Join(dir, "slinit-runner")
	build := exec.Command(gobin, "build", "-o", runner, "../../cmd/slinit-runner")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build runner: %v\n%s", err, out)
	}

	ln, err := net.Listen("unix", filepath.Join(dir, "sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	sockFile, err := ln.(*net.UnixListener).File()
	if err != nil {
		t.Fatal(err)
	}
	defer sockFile.Close()

	out := filepath.Join(dir, "env")
	p := ExecParams{
		RunnerPath: runner,
		SocketFD:   sockFile,
		Command:    []string{"/bin/sh", "-c", `echo "$LISTEN_FDS $LISTEN_PID $$" > ` + out},
	}
	if !needsRunnerWrap(p) {
		t.Fatal("a socket-activated service is not wrapped")
	}
	_, ch, err := StartProcess(p)
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	<-ch
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(b))
	if len(f) != 3 || f[0] != "1" || f[1] != f[2] {
		t.Errorf("LISTEN_FDS LISTEN_PID $$ = %q; want 1 and LISTEN_PID equal to the service's pid", strings.TrimSpace(string(b)))
	}
}
