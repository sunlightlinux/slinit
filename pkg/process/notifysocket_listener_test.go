package process

import (
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
)

// NewNotifySocketListener binds /run/slinit/notify/<svc>.sock, mode 0600,
// owned by the service's user, replacing a stale file a previous run
// left. Runs as root of a user + mount namespace with its own /run.
func TestNewNotifySocketListener(t *testing.T) {
	if !reexecAsRootWithMounts(t) {
		return
	}
	// The first listener makes the directory.
	first, err := NewNotifySocketListener("first", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	first.Stop()
	di, err := os.Stat("/run/slinit/notify")
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o755 {
		t.Errorf("notify dir mode %#o, want 0755", di.Mode().Perm())
	}

	path := "/run/slinit/notify/web.sock"
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	l, err := NewNotifySocketListener("web", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Stop()
	if l.Path() != path {
		t.Errorf("Path() = %q, want %q", l.Path(), path)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Type() != os.ModeSocket {
		t.Errorf("%s is %v, want a socket (the stale file was not replaced)", path, fi.Mode().Type())
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket mode %#o, want 0600", perm)
	}
	st := fi.Sys().(*syscall.Stat_t)
	if st.Uid != 0 || st.Gid != 0 {
		t.Errorf("socket owner %d:%d, want 0:0", st.Uid, st.Gid)
	}
	// It is a datagram socket that receives what a child sends.
	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("READY=1")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, err := l.conn.Read(buf)
	if err != nil || string(buf[:n]) != "READY=1" {
		t.Errorf("read %q, %v; want READY=1", buf[:n], err)
	}
}

func TestNewNotifySocketListenerErrors(t *testing.T) {
	if !reexecAsRootWithMounts(t) {
		return
	}
	// A name too long for sun_path: bind fails, nothing is left behind.
	long := strings.Repeat("n", 120)
	if _, err := NewNotifySocketListener(long, 0, 0); err == nil ||
		!strings.HasPrefix(err.Error(), "notify socket bind ") {
		t.Errorf("long name: err = %v, want a bind error", err)
	}
	if ents, _ := os.ReadDir("/run/slinit/notify"); len(ents) != 0 {
		t.Errorf("left behind after a failed bind: %v", ents)
	}

	// The directory cannot be made: reported as such.
	if err := os.RemoveAll("/run/slinit"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/run/slinit", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewNotifySocketListener("web", 0, 0); err == nil ||
		!strings.HasPrefix(err.Error(), "notify socket dir: ") {
		t.Errorf("blocked dir: err = %v, want a notify socket dir error", err)
	}
}
