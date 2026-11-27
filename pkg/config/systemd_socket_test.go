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
