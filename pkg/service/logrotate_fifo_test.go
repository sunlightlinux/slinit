package service

import (
	"os"
	"testing"
	"time"
)

// closeRotatorAfterLine feeds one line through a rotator built from
// cfg, then fails the test if Close does not return in two seconds.
// Close runs under the service set's lock on a restart, so a Close
// stuck behind the reader freezes PID 1. unblock is a FIFO to open on
// a timeout so the test can finish.
func closeRotatorAfterLine(t *testing.T, cfg LogRotatorConfig, unblock string) {
	t.Helper()
	cfg.ServiceName, cfg.FilePerms, cfg.FileUID, cfg.FileGID, cfg.LogLevelMax = "fifo", 0o600, -1, -1, -1
	lr, err := NewLogRotator(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w, err := lr.CreatePipe()
	if err != nil {
		t.Fatal(err)
	}
	lr.StartReader()
	w.Write([]byte("<2>hello\n"))
	lr.CloseWriteEnd()
	time.Sleep(200 * time.Millisecond)
	done := make(chan struct{})
	go func() { lr.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		if f, err := os.OpenFile(unblock, os.O_RDWR, 0); err == nil {
			f.Close()
		}
		<-done
		t.Fatal("LogRotator.Close blocked behind a FIFO with no reader")
	}
}

// The reader opened the log-file lazily with mu held; for a FIFO with
// no reader that open(2) never returned, nor did Close.
func TestLogRotatorFIFONoReader(t *testing.T) {
	f := mkfifo(t)
	closeRotatorAfterLine(t, LogRotatorConfig{FilePath: f}, f)
}

// The same for the alert file.
func TestLogRotatorAlertFIFONoReader(t *testing.T) {
	f := mkfifo(t)
	closeRotatorAfterLine(t, LogRotatorConfig{
		FilePath:      t.TempDir() + "/log",
		AlertFilePath: f,
		AlertLevel:    7,
	}, f)
}
