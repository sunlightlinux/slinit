package config

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// Loading runs with the loader's lock held, and a start or reload of
// any service waits for it. A FIFO where a description, overlay,
// override, include or executable in an init.d directory is read blocked
// the open until something opened its other end. Each now fails the
// load with "not a regular file".
func TestLoadServiceFIFODoesNotBlock(t *testing.T) {
	cases := []struct {
		name  string
		setup func(dl *DirLoader, dir string) string // returns the FIFO
	}{
		{"description", func(_ *DirLoader, dir string) string {
			return filepath.Join(dir, "svc")
		}},
		{"include", func(_ *DirLoader, dir string) string {
			p := filepath.Join(dir, "inc")
			writeFile(t, filepath.Join(dir, "svc"), "type = internal\n@include "+p+"\n")
			return p
		}},
		{"override", func(_ *DirLoader, dir string) string {
			writeFile(t, filepath.Join(dir, "svc"), "type = internal\n")
			return filepath.Join(dir, "svc.override")
		}},
		{"overlay", func(dl *DirLoader, dir string) string {
			writeFile(t, filepath.Join(dir, "svc"), "type = internal\n")
			od := t.TempDir()
			dl.SetOverlayDirs([]string{od})
			return filepath.Join(od, "svc")
		}},
		{"init.d script", func(dl *DirLoader, dir string) string {
			id := t.TempDir()
			dl.SetInitDDirs([]string{id})
			return filepath.Join(id, "svc")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			dl := NewDirLoader(service.NewServiceSet(&testReloadLogger{}), []string{dir})
			fifo := c.setup(dl, dir)
			// 0755: an init.d script is only looked at when executable.
			if err := syscall.Mkfifo(fifo, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(fifo, 0o755); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := dl.LoadService("svc")
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("loaded through a FIFO")
				}
				if c.name != "init.d script" && !strings.Contains(err.Error(), "not a regular file") {
					t.Errorf("err %v, want not a regular file", err)
				}
			case <-time.After(2 * time.Second):
				if f, err := os.OpenFile(fifo, os.O_RDWR, 0); err == nil {
					f.Close()
				}
				<-done
				t.Fatal("LoadService blocked on a FIFO")
			}
		})
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A description or override that is a symlink to /dev/null still
// reads as an empty file, as before.
func TestLoadServiceDevNullStillEmpty(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "svc"), "type = internal\n")
	if err := os.Symlink(os.DevNull, filepath.Join(dir, "svc.override")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(os.DevNull, filepath.Join(dir, "empty")); err != nil {
		t.Fatal(err)
	}
	dl := NewDirLoader(service.NewServiceSet(&testReloadLogger{}), []string{dir})
	for _, name := range []string{"svc", "empty"} {
		if _, err := dl.LoadService(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
