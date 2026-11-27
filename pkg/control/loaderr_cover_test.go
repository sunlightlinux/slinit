package control

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/config"
)

// The reply to a failed load tells slinitctl what to print: "not found",
// "description error" (the file does not parse) or "load error" (the
// file parses but the service cannot be set up). Each case through the
// real DirLoader. A missing description, the service's own or a
// dependency's, is NOSERVICE as in dinit (control.cc catches
// service_not_found); it was answered as a load error, and slinitctl
// said "could not be loaded" for a service that does not exist.
func TestLoadServiceErrorReplies(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	dir := t.TempDir()
	server.services.SetLoader(config.NewDirLoader(server.services, []string{dir}))

	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("badsyntax", "type = process\ncommand = /bin/true\nno-such-setting = 1\n")
	write("badtype", "type = nonsense\n")
	write("baddep", "type = internal\ndepends-on: missing-dep\n")
	write("good", "type = internal\n")

	conn := connectTest(t, sockPath)
	defer conn.Close()
	for _, tc := range []struct {
		name string
		want uint8
	}{
		{"absent", RplyNoService},
		{"badsyntax", RplyServiceDescErr},
		{"badtype", RplyServiceDescErr},
		{"baddep", RplyNoService},
		{"good", RplyServiceRecord},
	} {
		if err := WritePacket(conn, CmdLoadService, EncodeServiceName(tc.name)); err != nil {
			t.Fatal(err)
		}
		rply, _ := readReply(t, conn)
		if rply != tc.want {
			t.Errorf("load %q: reply %d, want %d", tc.name, rply, tc.want)
		}
	}
}

// syncBuf is a bytes.Buffer safe to write from the server's connection
// goroutine and read from the test.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A failed load over the control socket is answered with an opcode, so
// slinitctl can only print "could not be loaded" / "load error". The
// reason (file and line, the missing dependency) was logged nowhere and
// was simply lost; dinit logs "Could not load service ...". A name that
// does not exist at all is fully described by its reply and stays out
// of the log.
func TestLoadServiceFailureIsLogged(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	out := &syncBuf{}
	server.logger.SetOutput(out)
	dir := t.TempDir()
	server.services.SetLoader(config.NewDirLoader(server.services, []string{dir}))
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("badsyntax", "type = internal\nthis line is not a setting\n")
	write("baddep", "type = internal\ndepends-on: missing-dep\n")

	conn := connectTest(t, sockPath)
	defer conn.Close()
	for _, name := range []string{"badsyntax", "baddep", "absent"} {
		if err := WritePacket(conn, CmdLoadService, EncodeServiceName(name)); err != nil {
			t.Fatal(err)
		}
		readReply(t, conn)
	}
	log := out.String()
	for _, want := range []string{
		"Could not load service 'badsyntax'", filepath.Join(dir, "badsyntax") + ":2",
		"Could not load service 'baddep'", "missing-dep",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q; log:\n%s", want, log)
		}
	}
	if strings.Contains(log, "'absent'") {
		t.Errorf("a plain not-found was logged:\n%s", log)
	}
}
