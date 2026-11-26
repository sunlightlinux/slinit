package control

import (
	"os"
	"path/filepath"
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
