package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests run the built slinit as PID 1 of a fresh user, PID and
// mount namespace (`unshare -Urpfm --mount-proc`), where it can mount
// and be init without touching the host. They are skipped where user
// namespaces are not permitted, and under -short.
//
// SLINIT_TEST_BINDIR names a directory holding slinit and slinitctl to
// test instead of building them, e.g. an older build, to see a test
// fail on the bug it guards.

var nsBins struct {
	once sync.Once
	dir  string
	err  error
	out  []byte
}

func TestMain(m *testing.M) {
	code := m.Run()
	if nsBins.dir != "" && os.Getenv("SLINIT_TEST_BINDIR") == "" {
		os.RemoveAll(nsBins.dir)
	}
	os.Exit(code)
}

// nsBinDir returns the directory holding slinit and slinitctl, building
// them on first use, or skips the test when a namespace cannot be had.
func nsBinDir(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("namespace e2e test skipped under -short")
	}
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not available")
	}
	if err := exec.Command("unshare", "-Urpfm", "--mount-proc", "true").Run(); err != nil {
		t.Skipf("unshare -Urpfm not permitted here: %v", err)
	}
	nsBins.once.Do(func() {
		if d := os.Getenv("SLINIT_TEST_BINDIR"); d != "" {
			nsBins.dir = d
			return
		}
		gobin, err := exec.LookPath("go")
		if err != nil {
			nsBins.err = err
			return
		}
		nsBins.dir, nsBins.err = os.MkdirTemp("", "slinit-nsbin-")
		if nsBins.err != nil {
			return
		}
		for _, pkg := range []string{".", "../slinitctl"} {
			name := filepath.Base(pkg)
			if pkg == "." {
				name = "slinit"
			}
			cmd := exec.Command(gobin, "build", "-o", filepath.Join(nsBins.dir, name), pkg)
			if nsBins.out, nsBins.err = cmd.CombinedOutput(); nsBins.err != nil {
				return
			}
		}
	})
	if nsBins.err != nil {
		if len(nsBins.out) > 0 {
			t.Fatalf("build: %v\n%s", nsBins.err, nsBins.out)
		}
		t.Skipf("cannot build slinit: %v", nsBins.err)
	}
	return nsBins.dir
}

// nsRun is a namespace running a shell script, slinit usually exec'd
// at its end. $B is the binary directory and $D the test's directory.
type nsRun struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	stdout bytes.Buffer
	stderr bytes.Buffer
	done   chan struct{}
}

// startNS starts script in a new namespace; extra become its fds 3 on.
func startNS(t *testing.T, dir, script string, extra ...*os.File) *nsRun {
	t.Helper()
	bin := nsBinDir(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	r := &nsRun{cancel: cancel, done: make(chan struct{})}
	r.cmd = exec.CommandContext(ctx, "unshare", "-Urpfm", "--mount-proc", "--kill-child",
		"sh", "-c", script, "sh")
	r.cmd.Env = append(os.Environ(), "B="+bin, "D="+dir)
	r.cmd.Stdout = &r.stdout
	r.cmd.Stderr = &r.stderr
	r.cmd.ExtraFiles = extra
	if err := r.cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start namespace: %v", err)
	}
	go func() { r.cmd.Wait(); close(r.done) }()
	t.Cleanup(r.stop)
	return r
}

// stop kills the namespace (its PID 1 with it) and waits for it.
func (r *nsRun) stop() {
	r.cancel()
	<-r.done
}

// wait waits up to d for the namespace to end by itself.
func (r *nsRun) wait(d time.Duration) bool {
	select {
	case <-r.done:
		return true
	case <-time.After(d):
		return false
	}
}

// waitFile waits up to d for path to exist with something in it.
func waitFile(t *testing.T, path string, d time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			// Give a writer still running a moment to finish.
			time.Sleep(50 * time.Millisecond)
			b, _ = os.ReadFile(path)
			return string(b)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return ""
}

// writeService writes a service description into dir/sd.
func writeService(t *testing.T, dir, name, body string) {
	t.Helper()
	sd := filepath.Join(dir, "sd")
	if err := os.MkdirAll(sd, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sd, name), []byte(strings.TrimLeft(body, "\n")), 0644); err != nil {
		t.Fatal(err)
	}
}
