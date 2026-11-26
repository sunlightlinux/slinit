package safepath

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

// inRootCases runs fn with openat2 and with the walk that stands in for
// it on kernels without one.
func inRootCases(t *testing.T, fn func(t *testing.T)) {
	t.Run("openat2", fn)
	t.Run("walk", func(t *testing.T) {
		openat2 = func(int, string, *unix.OpenHow) (int, error) { return -1, unix.ENOSYS }
		defer func() { openat2 = unix.Openat2 }()
		fn(t)
	})
}

// readInRoot reads path inside root through OpenInRoot.
func readInRoot(t *testing.T, root, path string) (string, error) {
	t.Helper()
	fd, err := OpenInRoot(root, path, unix.O_RDONLY)
	if err != nil {
		return "", err
	}
	defer unix.Close(fd)
	b, err := os.ReadFile("/proc/self/fd/" + strconv.Itoa(fd))
	return string(b), err
}

// A tree's absolute symlinks and ".." resolve inside it, never on the
// host: an image's run/x -> /etc/secret names its own etc/secret.
func TestOpenInRootStaysInRoot(t *testing.T) {
	inRootCases(t, func(t *testing.T) {
		host := t.TempDir()
		os.WriteFile(filepath.Join(host, "secret"), []byte("host"), 0o600)
		root := t.TempDir()
		os.MkdirAll(filepath.Join(root, "run", "sub"), 0o755)
		os.MkdirAll(filepath.Join(root, host), 0o755)
		os.WriteFile(filepath.Join(root, host, "secret"), []byte("image"), 0o600)
		os.Symlink(filepath.Join(host, "secret"), filepath.Join(root, "run/abs"))
		os.Symlink("../../../../../../.."+host+"/secret", filepath.Join(root, "run/sub/rel"))
		os.Symlink(host, filepath.Join(root, "run/dir"))
		for _, p := range []string{"run/abs", "/run/sub/rel", "../../run/dir/secret", host + "/secret"} {
			got, err := readInRoot(t, root, p)
			if err != nil {
				t.Errorf("%s: %v", p, err)
			} else if got != "image" {
				t.Errorf("%s resolved outside the root: read %q", p, got)
			}
		}
		os.Remove(filepath.Join(root, host, "secret"))
		if got, err := readInRoot(t, root, "run/abs"); err == nil {
			t.Errorf("run/abs with no target in the root read %q", got)
		}
	})
}

func TestOpenInRootDirectory(t *testing.T) {
	inRootCases(t, func(t *testing.T) {
		root := t.TempDir()
		os.MkdirAll(filepath.Join(root, "var/log/j"), 0o755)
		os.WriteFile(filepath.Join(root, "var/log/f"), nil, 0o600)
		fd, err := OpenInRoot(root, "/var/log/j", unix.O_RDONLY|unix.O_DIRECTORY)
		if err != nil {
			t.Fatal(err)
		}
		unix.Close(fd)
		if fd, err := OpenInRoot(root, "/var/log/f", unix.O_RDONLY|unix.O_DIRECTORY); err == nil {
			unix.Close(fd)
			t.Error("a file opened as a directory")
		}
	})
}
