package machine

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestHostRootPrefersExplicitRoot(t *testing.T) {
	m := &Machine{Name: "x", PID: 42, Root: "/mnt/rootfs"}
	got := m.hostRoot()
	if got != "/mnt/rootfs" {
		t.Errorf("hostRoot with explicit Root = %q, want /mnt/rootfs", got)
	}
}

func TestHostRootFallsBackToProc(t *testing.T) {
	m := &Machine{Name: "x", PID: 42}
	got := m.hostRoot()
	if got != "/proc/42/root" {
		t.Errorf("hostRoot with no Root = %q, want /proc/42/root", got)
	}
}

func TestListJournalFilesEmptyRootReturnsNil(t *testing.T) {
	dir := t.TempDir()
	m := &Machine{Name: "empty", PID: 42, Root: dir}
	files, err := m.ListJournalFiles()
	if err != nil {
		t.Fatal(err)
	}
	if files != nil {
		t.Errorf("files = %v, want nil for empty root", files)
	}
}

func TestListJournalFilesPicksUpJSONL(t *testing.T) {
	dir := t.TempDir()
	journal := filepath.Join(dir, "var/log/slinit-journal")
	if err := os.MkdirAll(journal, 0o755); err != nil {
		t.Fatal(err)
	}
	// Files that should be picked up
	for _, f := range []string{"a.jsonl", "b.jsonl.gz", "c.slj"} {
		if err := os.WriteFile(filepath.Join(journal, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Files that should NOT be picked up
	for _, f := range []string{"README", "index.idx"} {
		if err := os.WriteFile(filepath.Join(journal, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := &Machine{Name: "e", PID: 42, Root: dir}
	files, err := m.ListJournalFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("files=%v, want 3 entries", files)
	}
	// Ordering is lex (which == chrono for YYYY-MM-DD-style names)
	if !strings.HasSuffix(files[0], "a.jsonl") ||
		!strings.HasSuffix(files[1], "b.jsonl.gz") ||
		!strings.HasSuffix(files[2], "c.slj") {
		t.Errorf("ordering off: %v", files)
	}
}

// The journal directories were read at root+path, which follows the
// container's symlinks on the host: a container whose
// var/log/slinit-journal pointed at a host directory, or whose journal
// file was a link to a host file, had the host's journal shown as its
// own. They resolve inside the root now, and only regular files count.
func TestListJournalFilesStaysInsideRoot(t *testing.T) {
	host := t.TempDir()
	if err := os.WriteFile(filepath.Join(host, "host.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "var/log"), 0o755)
	os.MkdirAll(filepath.Join(root, "run/slinit-journal"), 0o755)
	if err := os.Symlink(host, filepath.Join(root, "var/log/slinit-journal")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(host, "host.jsonl"), filepath.Join(root, "run/slinit-journal/link.jsonl")); err != nil {
		t.Fatal(err)
	}
	m := &Machine{Name: "c", PID: 42, Root: root}
	files, err := m.ListJournalFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		t.Errorf("listed %s, which reads the host's %q", f, b)
	}
}

// slinit's binary journal files are *.journal; a container that kept only
// those was reported to have no persistent journal.
func TestListJournalFilesPicksUpBinary(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "var/log/slinit-journal")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "system.journal"), []byte("x"), 0o644)
	files, err := (&Machine{Name: "c", PID: 42, Root: root}).ListJournalFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || !strings.HasSuffix(files[0], "system.journal") {
		t.Errorf("files = %v; want system.journal", files)
	}
}

// A container's /run/slinit.socket -> /run/slinit.socket resolved on the
// host and reached the host's control socket. socketIn resolves it inside
// the container's root; a socket that is there is reached through it.
func TestSocketInStaysInsideRoot(t *testing.T) {
	host := t.TempDir()
	hostSock := filepath.Join(host, "slinit.socket")
	l, err := net.Listen("unix", hostSock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "run"), 0o755)
	if err := os.Symlink(hostSock, filepath.Join(root, "run/slinit.socket")); err != nil {
		t.Fatal(err)
	}
	if fd, err := socketIn(root, ControlSockPath); err == nil {
		unix.Close(fd)
		t.Fatal("a symlink to a host socket resolved outside the container root")
	}

	os.Remove(filepath.Join(root, "run/slinit.socket"))
	l2, err := net.Listen("unix", filepath.Join(root, "run/slinit.socket"))
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	fd, err := socketIn(root, ControlSockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	c, err := net.Dial("unix", fdPath(fd))
	if err != nil {
		t.Fatalf("dial through %s: %v", fdPath(fd), err)
	}
	c.Close()
}
