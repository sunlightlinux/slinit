package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestParseIOPrio(t *testing.T) {
	tests := []struct {
		input     string
		wantClass int
		wantLevel int
	}{
		{"be:4", 2, 4},
		{"best-effort:0", 2, 0},
		{"rt:7", 1, 7},
		{"realtime:3", 1, 3},
		{"idle", 3, 0},
		{"2:5", 2, 5},
		{"invalid", -1, 0},
		{"be", 2, 0},
	}

	for _, tt := range tests {
		class, level := parseIOPrio(tt.input)
		if class != tt.wantClass || level != tt.wantLevel {
			t.Errorf("parseIOPrio(%q): got (%d, %d), want (%d, %d)",
				tt.input, class, level, tt.wantClass, tt.wantLevel)
		}
	}
}

func TestParseRlimitHelper(t *testing.T) {
	tests := []struct {
		input    string
		wantSoft uint64
		wantHard uint64
		wantErr  bool
	}{
		{"1024", 1024, 1024, false},
		{"1024:4096", 1024, 4096, false},
		{"unlimited", ^uint64(0), ^uint64(0), false},
		{"abc", 0, 0, true},
	}

	for _, tt := range tests {
		lim, err := parseRlimit(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseRlimit(%q): error = %v, wantErr = %v", tt.input, err, tt.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		if lim[0] != tt.wantSoft || lim[1] != tt.wantHard {
			t.Errorf("parseRlimit(%q): got [%d, %d], want [%d, %d]",
				tt.input, lim[0], lim[1], tt.wantSoft, tt.wantHard)
		}
	}
}

// TestResolveAlertLevel pins the fallback matrix: file empty →
// disabled (-1), file set + level parsed → level unchanged, file set +
// level default (-1) → warn (4) so operators declaring only the file
// still get sane routing.
func TestResolveAlertLevel(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		parsed   int
		expected int
	}{
		{"no-file-no-level", "", -1, -1},
		{"no-file-with-level", "", 3, -1},
		{"file-no-level", "/tmp/a.log", -1, 4},
		{"file-with-level", "/tmp/a.log", 2, 2},
		{"file-with-emerg", "/tmp/a.log", 0, 0},
	}
	for _, tt := range tests {
		got := resolveAlertLevel(tt.file, tt.parsed)
		if got != tt.expected {
			t.Errorf("%s: resolveAlertLevel(%q, %d) = %d, want %d",
				tt.name, tt.file, tt.parsed, got, tt.expected)
		}
	}
}

func TestSocketMulticastGroupRequiresUDPListener(t *testing.T) {
	// A membership can only be added to a datagram socket, so the
	// directive must be refused where it would be stored and never used.
	cases := []struct {
		name    string
		listen  string
		wantErr bool
	}{
		{"udp accepted", "socket-listen = udp:0.0.0.0:5353", false},
		{"udp4 accepted", "socket-listen = udp4:0.0.0.0:5353", false},
		{"udp6 accepted", "socket-listen = udp6:[::]:5353", false},
		{"tcp refused", "socket-listen = tcp:0.0.0.0:5353", true},
		{"unix refused", "socket-listen = /run/test.sock", true},
		{"no listener refused", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			body := "type = process\ncommand = /bin/true\n" +
				"socket-multicast-group = 224.0.0.251\n"
			if c.listen != "" {
				body += c.listen + "\n"
			}
			if err := os.WriteFile(filepath.Join(dir, "mcast"), []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
			set := service.NewServiceSet(&testReloadLogger{})
			dl := NewDirLoader(set, []string{dir})
			_, err := dl.LoadService("mcast")
			if c.wantErr && err == nil {
				t.Error("loaded with no udp listener, want refusal")
			}
			if !c.wantErr && err != nil {
				t.Errorf("refused a udp listener: %v", err)
			}
			if c.wantErr && err != nil &&
				!strings.Contains(err.Error(), "socket-multicast-group") {
				t.Errorf("error does not name the directive: %v", err)
			}
		})
	}
}

func TestSocketAcceptRequiresAnAcceptableSocket(t *testing.T) {
	cases := []struct {
		name    string
		listen  string
		wantErr bool
	}{
		{"tcp accepted", "socket-listen = tcp:0.0.0.0:7777", false},
		{"unix accepted", "socket-listen = /run/test-accept.sock", false},
		{"udp refused", "socket-listen = udp:0.0.0.0:7777", true},
		{"udp4 refused", "socket-listen = udp4:0.0.0.0:7777", true},
		{"no listener refused", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			body := "type = process\ncommand = /bin/true\nsocket-accept = yes\n"
			if c.listen != "" {
				body += c.listen + "\n"
			}
			if err := os.WriteFile(filepath.Join(dir, "acc"), []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
			set := service.NewServiceSet(&testReloadLogger{})
			dl := NewDirLoader(set, []string{dir})
			_, err := dl.LoadService("acc")
			if c.wantErr && err == nil {
				t.Error("loaded a service socket-accept cannot work on")
			}
			if !c.wantErr && err != nil {
				t.Errorf("refused a stream listener: %v", err)
			}
			if c.wantErr && err != nil &&
				!strings.Contains(err.Error(), "socket-accept") {
				t.Errorf("error does not name the directive: %v", err)
			}
		})
	}
}

func TestSocketAcceptParsesBothWays(t *testing.T) {
	for _, c := range []struct {
		val  string
		want bool
	}{{"yes", true}, {"no", false}, {"true", true}, {"false", false}} {
		dir := t.TempDir()
		body := "type = process\ncommand = /bin/true\n" +
			"socket-listen = tcp:0.0.0.0:7777\nsocket-accept = " + c.val + "\n"
		if err := os.WriteFile(filepath.Join(dir, "acc"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		set := service.NewServiceSet(&testReloadLogger{})
		dl := NewDirLoader(set, []string{dir})
		svc, err := dl.LoadService("acc")
		if err != nil {
			t.Fatalf("socket-accept = %s: %v", c.val, err)
		}
		if got := svc.Record().SocketAccept(); got != c.want {
			t.Errorf("socket-accept = %s gave %v, want %v", c.val, got, c.want)
		}
	}
}

// A cycle through a dependency that starts its target used to load without
// error: the service is in the set before its dependencies load, so the
// recursive load found it there and returned it, and the "circular
// dependency" check never ran. Started, both services then waited on each
// other in STARTING forever. dinit refuses these at load time.
func TestLoadRejectsDependencyCycle(t *testing.T) {
	for _, kw := range []string{"depends-on", "depends-ms", "waits-for", "prepared-by"} {
		t.Run(kw, func(t *testing.T) {
			dir := t.TempDir()
			ss := service.NewServiceSet(&testReloadLogger{})
			loader := NewDirLoader(ss, []string{dir})
			ss.SetLoader(loader)
			writeServiceFile(t, dir, "a", "type = internal\n"+kw+": b\n")
			writeServiceFile(t, dir, "b", "type = internal\n"+kw+": a\n")

			if _, err := loader.LoadService("a"); err == nil || !strings.Contains(err.Error(), "cyclic dependency") {
				t.Fatalf("LoadService(a) = %v, want a cyclic dependency error", err)
			}
			for _, n := range []string{"a", "b"} {
				if ss.FindService(n, true) != nil {
					t.Errorf("%s left in the set after a failed load", n)
				}
			}
		})
	}

	t.Run("self", func(t *testing.T) {
		dir := t.TempDir()
		ss := service.NewServiceSet(&testReloadLogger{})
		loader := NewDirLoader(ss, []string{dir})
		writeServiceFile(t, dir, "a", "type = internal\ndepends-on: a\n")
		if _, err := loader.LoadService("a"); err == nil {
			t.Fatal("a service depending on itself loaded")
		}
	})

	t.Run("dir", func(t *testing.T) {
		dir := t.TempDir()
		ss := service.NewServiceSet(&testReloadLogger{})
		loader := NewDirLoader(ss, []string{dir})
		if err := os.Mkdir(filepath.Join(dir, "c.d"), 0755); err != nil {
			t.Fatal(err)
		}
		writeServiceFile(t, filepath.Join(dir, "c.d"), "a", "")
		writeServiceFile(t, dir, "a", "type = internal\ndepends-on: b\n")
		writeServiceFile(t, dir, "b", "type = internal\nwaits-for: c\n")
		writeServiceFile(t, dir, "c", "type = internal\ndepends-on.d: c.d\n")
		if _, err := loader.LoadService("a"); err == nil || !strings.Contains(err.Error(), "cyclic dependency") {
			t.Fatalf("LoadService(a) = %v, want a cyclic dependency error", err)
		}
		for _, n := range []string{"a", "b", "c"} {
			if ss.FindService(n, true) != nil {
				t.Errorf("%s left in the set after a failed load", n)
			}
		}
	})
}

// Ordering is not a cycle: a after b and b before a say the same thing, and
// loading one loads the other while the first is still loading.
func TestLoadAllowsBeforeAfterPair(t *testing.T) {
	dir := t.TempDir()
	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	writeServiceFile(t, dir, "a", "type = internal\nafter: b\n")
	writeServiceFile(t, dir, "b", "type = internal\nbefore: a\n")
	if _, err := loader.LoadService("a"); err != nil {
		t.Fatalf("LoadService(a): %v", err)
	}
}
