package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func sockConvert(t *testing.T, dir, svcUnit string) (*ServiceDescription, []SystemdWarning) {
	t.Helper()
	p := writeUnit(t, dir, "foo.service", svcUnit)
	_, warns, err := ConvertSystemdUnitIn(p, []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	desc, err := SystemdUnitToServiceDescription(p, "foo", dir)
	if err != nil {
		t.Fatal(err)
	}
	return desc, warns
}

func sockWarned(warns []SystemdWarning, sub string) bool {
	for _, w := range warns {
		if strings.Contains(w.Msg, sub) {
			return true
		}
	}
	return false
}

const sockSvc = "[Service]\nExecStart=/usr/bin/foo\n"

func TestSystemdSocketPairIsRead(t *testing.T) {
	dir := t.TempDir()
	writeUnit(t, dir, "foo.socket", "[Socket]\nListenStream=/run/foo/foo.sock\nListenStream=8080\n"+
		"ListenStream=127.0.0.1:9090\nListenDatagram=[::1]:53\nSocketMode=0660\nSocketUser=0\nSocketGroup=0\n"+
		"Backlog=128\nFileDescriptorName=foo\n[Install]\nWantedBy=sockets.target\n")
	desc, warns := sockConvert(t, dir, sockSvc)
	want := []string{"/run/foo/foo.sock", "tcp:[::]:8080", "tcp:127.0.0.1:9090", "udp:[::1]:53"}
	if !slices.Equal(desc.SocketPaths, want) {
		t.Errorf("socket-listen %q, want %q", desc.SocketPaths, want)
	}
	if desc.SocketPerms != 0o660 || desc.SocketUID != 0 || desc.SocketGID != 0 {
		t.Errorf("perms %o uid %d gid %d", desc.SocketPerms, desc.SocketUID, desc.SocketGID)
	}
	if !sockWarned(warns, "FileDescriptorName=foo not carried") || sockWarned(warns, "Backlog") {
		t.Errorf("notes: %v", warns)
	}
}

func TestSystemdSocketEdges(t *testing.T) {
	for _, tc := range []struct {
		name, socket, svc string
		dropin            string
		listens           []string
		warn              string
	}{
		{name: "no socket unit", svc: sockSvc},
		{name: "a reset", socket: "[Socket]\nListenStream=/run/a\nListenStream=\nListenStream=/run/b\n", svc: sockSvc,
			listens: []string{"/run/b"}},
		{name: "drop-in adds", socket: "[Socket]\nListenStream=/run/a\n", dropin: "[Socket]\nListenStream=/run/b\n", svc: sockSvc,
			listens: []string{"/run/a", "/run/b"}},
		{name: "unix datagram", socket: "[Socket]\nListenDatagram=/run/d\nListenStream=/run/s\n", svc: sockSvc,
			listens: []string{"/run/s"}, warn: "no Unix datagram"},
		{name: "abstract", socket: "[Socket]\nListenStream=@foo\n", svc: sockSvc, warn: "abstract"},
		{name: "fifo", socket: "[Socket]\nListenFIFO=/run/f\n", svc: sockSvc, warn: "only stream and datagram"},
		{name: "accept", socket: "[Socket]\nListenStream=22\nAccept=yes\n", svc: sockSvc, warn: "Accept=yes"},
		{name: "for another service", socket: "[Socket]\nListenStream=/run/a\nService=bar.service\n", svc: sockSvc,
			warn: "is for bar.service"},
		{name: "Sockets= names another", svc: "[Service]\nExecStart=/usr/bin/foo\nSockets=other.socket\n",
			listens: []string{"/run/other"}},
		{name: "Sockets= missing", svc: "[Service]\nExecStart=/usr/bin/foo\nSockets=gone.socket\n", warn: "no such socket unit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.socket != "" {
				writeUnit(t, dir, "foo.socket", tc.socket)
			}
			writeUnit(t, dir, "other.socket", "[Socket]\nListenStream=/run/other\n")
			if tc.dropin != "" {
				os.MkdirAll(filepath.Join(dir, "foo.socket.d"), 0o755)
				os.WriteFile(filepath.Join(dir, "foo.socket.d", "x.conf"), []byte(tc.dropin), 0o644)
			}
			desc, warns := sockConvert(t, dir, tc.svc)
			if !slices.Equal(desc.SocketPaths, tc.listens) {
				t.Errorf("socket-listen %q, want %q", desc.SocketPaths, tc.listens)
			}
			if tc.warn != "" && !sockWarned(warns, tc.warn) {
				t.Errorf("no warning containing %q in %v", tc.warn, warns)
			}
		})
	}
}

// foo.socket with Accept=yes and the template foo@.service, no
// foo.service: loading foo is systemd's per-connection service, the
// template's command run once per connection (socket-accept).
func TestSystemdAcceptSocketWithTemplate(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "units")
	writeUnit(t, units, "lpd.socket", "[Socket]\nListenStream=515\nAccept=yes\n")
	writeUnit(t, units, "lpd@.service", "[Unit]\nRequires=lpd.socket\n[Service]\nExecStart=-/usr/lib/cups/daemon/cups-lpd\nStandardInput=socket\n")
	_, dl, _ := cycleLoader(t, root)
	desc, path, err := dl.findAndParse("lpd")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "lpd@.service" || !desc.SocketAccept || !slices.Equal(desc.SocketPaths, []string{"tcp:[::]:515"}) {
		t.Errorf("path %s accept %v listen %q", path, desc.SocketAccept, desc.SocketPaths)
	}
	if !dl.descriptionExists("lpd") {
		t.Error("descriptionExists does not see the accept pair")
	}

	// Accept=no with only a template: nothing to load.
	writeUnit(t, units, "x.socket", "[Socket]\nListenStream=516\n")
	writeUnit(t, units, "x@.service", "[Service]\nExecStart=/bin/x\n")
	if _, _, err := dl.findAndParse("x"); err == nil {
		t.Error("a template behind an Accept=no socket loaded as x")
	}
	// The converter reads it the same way.
	cfg, warns, err := ConvertSystemdAcceptUnitIn(filepath.Join(units, "lpd@.service"), "lpd", []string{units})
	if err != nil || cfg.svcName != "lpd" {
		t.Fatalf("%v %v", cfg, err)
	}
	if sockWarned(warns, "StandardInput") {
		t.Errorf("StandardInput=socket noted as a gap under socket-accept: %v", warns)
	}
}

// No SocketMode=: systemd's 0666, not slinit's 0600, or clients that
// are not root are turned away.
func TestSystemdSocketDefaultMode(t *testing.T) {
	dir := t.TempDir()
	writeUnit(t, dir, "foo.socket", "[Socket]\nListenStream=/run/foo.sock\n")
	desc, _ := sockConvert(t, dir, sockSvc)
	if desc.SocketPerms != 0o666 {
		t.Errorf("socket-permissions %o, want 0666", desc.SocketPerms)
	}
}

// A template behind an Accept=yes socket with no listener slinit can
// open is not loaded as a service with no socket.
func TestSystemdAcceptWithoutListenerIsNotLoaded(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "units")
	writeUnit(t, units, "cd.socket", "[Socket]\nListenSequentialPacket=/run/cd\nAccept=yes\n")
	writeUnit(t, units, "cd@.service", "[Service]\nExecStart=/bin/cd\n")
	_, dl, _ := cycleLoader(t, root)
	if _, _, err := dl.findAndParse("cd"); err == nil || !strings.Contains(err.Error(), "no listener") {
		t.Errorf("err %v, want refused for having no listener", err)
	}
}
