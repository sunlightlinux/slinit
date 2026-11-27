package process

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// SetupCredentials runs in StartProcess, inside the start, with the
// service set's lock held. A load-credential source that was a FIFO
// blocked the read until something wrote to it; one that was a device
// such as /dev/zero was read without end. Only regular files are read,
// up to systemd's CREDENTIAL_SIZE_MAX.
func TestSetupCredentialsNonRegularSource(t *testing.T) {
	if !reexecAsRootWithMounts(t) {
		return
	}
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{fifo, "/dev/zero"} {
		done := make(chan error, 1)
		go func() {
			_, err := SetupCredentials("nonreg", []CredentialSource{{Name: "c", Path: src}}, 0, 0)
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "not a regular file") {
				t.Errorf("%s: err %v, want not a regular file", src, err)
			}
		case <-time.After(2 * time.Second):
			if w, err := os.OpenFile(fifo, os.O_RDWR, 0); err == nil {
				w.Close()
			}
			t.Fatalf("SetupCredentials blocked on %s", src)
		}
		CleanupCredentials("nonreg")
	}
}
