package process

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A standard-input-file FIFO is opened without waiting for a writer, so
// that PID 1 cannot block in open(2). Opened read-only that way, the
// child saw end-of-file at once whenever no writer happened to be
// connected, and a daemon fed through the FIFO exited at start. The
// child must instead wait for data, as it did when the open blocked.
func TestStdinFileFIFOWaitsForData(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "in")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	pid, ch, err := StartProcess(ExecParams{
		ServiceName: "svc",
		Command:     []string{"/bin/sh", "-c", "read l; echo \"$l\" > " + out},
		StdinFile:   fifo,
	})
	if err != nil {
		t.Fatal(err)
	}
	exited := false
	defer func() {
		if !exited {
			syscall.Kill(pid, syscall.SIGKILL)
			<-ch
		}
	}()

	select {
	case <-ch:
		exited = true
		t.Fatal("the service exited before anything was written: it read end-of-file from the FIFO")
	case <-time.After(300 * time.Millisecond):
	}

	w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatalf("open FIFO for writing: %v", err)
	}
	w.WriteString("hello\n")
	w.Close()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, _ := os.ReadFile(out); string(b) == "hello\n" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the service never received the line written to the FIFO")
}
