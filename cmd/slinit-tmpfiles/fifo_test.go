package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// F and w opened their target O_WRONLY without O_NONBLOCK, so a FIFO
// at the path blocked open(2) until a reader came: forever, at boot.
// Both must refuse it, promptly.
func TestFileAndWriteRefuseFifo(t *testing.T) {
	for _, kind := range []string{"F", "w"} {
		t.Run(kind, func(t *testing.T) {
			fifo := filepath.Join(t.TempDir(), "fifo")
			if err := syscall.Mkfifo(fifo, 0o644); err != nil {
				t.Fatal(err)
			}
			res := make(chan error, 1)
			go func() { res <- apply(entry{kind: kind, path: fifo, mode: 0o644, uid: -1, gid: -1, arg: "x"}) }()
			select {
			case err := <-res:
				if err == nil {
					t.Errorf("%s on a FIFO: no error, want a refusal", kind)
				}
			case <-time.After(5 * time.Second):
				// Unblock the stuck open so the goroutine can finish.
				if f, err := os.OpenFile(fifo, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
					defer f.Close()
				}
				t.Fatalf("%s on a FIFO hung in open(2)", kind)
			}
		})
	}
}

// w writes into a file that exists and skips a missing one, as systemd
// does; it used to create it.
func TestWriteSkipsMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	if err := apply(entry{kind: "w", path: path, arg: "x"}); err != nil {
		t.Fatalf("w on a missing file: %v, want it skipped", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("w created %s: %v", path, err)
	}
}
