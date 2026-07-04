package mounts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/fstab"
)

func TestLookupNetdev(t *testing.T) {
	entries, err := fstab.Parse(strings.NewReader(
		"/dev/sda1 / ext4 defaults 0 1\n" +
			"//srv/nfs /mnt/nfs nfs _netdev,ro 0 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := LookupNetdev(entries, "/"); got != NetdevNo {
		t.Errorf("/ = %d, want NetdevNo", got)
	}
	if got := LookupNetdev(entries, "/mnt/nfs"); got != NetdevYes {
		t.Errorf("/mnt/nfs = %d, want NetdevYes", got)
	}
	if got := LookupNetdev(entries, "/nope"); got != NetdevUnknown {
		t.Errorf("/nope = %d, want NetdevUnknown", got)
	}
}

// TestReadAndIsMountedViaFixture points DefaultPath at a fixture so
// the test doesn't rely on the environment.
func TestReadAndIsMountedViaFixture(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mounts")
	fixture := "/dev/sda1 / ext4 rw,relatime 0 0\n" +
		"proc /proc proc rw 0 0\n" +
		"tmpfs /tmp tmpfs rw,mode=1777 0 0\n"
	if err := os.WriteFile(path, []byte(fixture), 0644); err != nil {
		t.Fatal(err)
	}
	saved := DefaultPath
	DefaultPath = path
	t.Cleanup(func() { DefaultPath = saved })

	entries, err := Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Errorf("len=%d, want 3", len(entries))
	}
	for _, want := range []string{"/", "/proc", "/tmp"} {
		ok, err := IsMounted(want)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Errorf("IsMounted(%q) = false", want)
		}
	}
	if ok, _ := IsMounted("/nope"); ok {
		t.Errorf("IsMounted(/nope) = true")
	}
}
