package config

import (
	"strings"
	"syscall"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// TestParseSandboxAll exercises every sandbox stanza in one go and
// verifies the parsed fields. Coverage for the per-stanza error paths
// lives in the dedicated tests below.
func TestParseSandboxAll(t *testing.T) {
	input := `type = process
command = /usr/bin/svc
private-tmp = yes
protect-system = full
read-only-paths = /usr/local /opt
read-write-paths = /var/lib/svc
read-write-paths = /var/log/svc
`
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !desc.PrivateTmp {
		t.Error("PrivateTmp should be true")
	}
	if desc.ProtectSystem != "full" {
		t.Errorf("ProtectSystem = %q, want full", desc.ProtectSystem)
	}
	wantRO := []string{"/usr/local", "/opt"}
	if !equalStrings(desc.ReadOnlyPaths, wantRO) {
		t.Errorf("ReadOnlyPaths = %v, want %v", desc.ReadOnlyPaths, wantRO)
	}
	wantRW := []string{"/var/lib/svc", "/var/log/svc"}
	if !equalStrings(desc.ReadWritePaths, wantRW) {
		t.Errorf("ReadWritePaths = %v, want %v", desc.ReadWritePaths, wantRW)
	}
}

// TestParseProtectSystemLevels checks the four accepted level strings
// and a couple of synonyms, plus rejection of an unknown level.
func TestParseProtectSystemLevels(t *testing.T) {
	for _, c := range []struct {
		in, want string
	}{
		{"yes", "yes"},
		{"true", "yes"},
		{"1", "yes"},
		{"full", "full"},
		{"strict", "strict"},
		{"no", ""},
		{"off", ""},
		{"", ""},
	} {
		input := "type = process\ncommand = /bin/true\nprotect-system = " + c.in + "\n"
		desc, err := Parse(strings.NewReader(input), "svc", "test-file")
		if err != nil {
			t.Errorf("level %q: parse failed: %v", c.in, err)
			continue
		}
		if desc.ProtectSystem != c.want {
			t.Errorf("level %q → %q, want %q", c.in, desc.ProtectSystem, c.want)
		}
	}
	bad := "type = process\ncommand = /bin/true\nprotect-system = paranoid\n"
	if _, err := Parse(strings.NewReader(bad), "svc", "test-file"); err == nil {
		t.Fatal("expected parse error for unknown protect-system level")
	}
}

// TestParseSandboxPathsRejectsRelative ensures path stanzas refuse
// non-absolute paths — the runner applies them in a chrooted-style mount
// namespace and a relative path would be ambiguous.
func TestParseSandboxPathsRejectsRelative(t *testing.T) {
	for _, key := range []string{"read-only-paths", "read-write-paths"} {
		input := "type = process\ncommand = /bin/true\n" + key + " = etc/conf\n"
		_, err := Parse(strings.NewReader(input), "svc", "test-file")
		if err == nil || !strings.Contains(err.Error(), "must be absolute") {
			t.Errorf("%s relative path: want absolute-path error, got %v", key, err)
		}
	}
}

// TestParseSandboxPathsRejectsTraversal ensures '..' components are
// caught at parse time rather than letting the runner discover the
// escape attempt.
func TestParseSandboxPathsRejectsTraversal(t *testing.T) {
	input := "type = process\ncommand = /bin/true\nread-only-paths = /etc/../root\n"
	_, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err == nil || !strings.Contains(err.Error(), "..") {
		t.Fatalf("expected traversal error, got %v", err)
	}
}

// TestSandboxFlowsToRecord exercises the loader path: sandbox settings
// reach the ServiceRecord and trigger the automatic CLONE_NEWNS that the
// runner needs to see a fresh mount namespace.
func TestSandboxFlowsToRecord(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "boxed",
		"type = process\ncommand = /usr/bin/boxed\n"+
			"private-tmp = yes\nprotect-system = yes\n"+
			"read-only-paths = /usr/local\nread-write-paths = /var/lib/boxed\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	svc, err := loader.LoadService("boxed")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	rec := svc.Record()
	if !rec.SandboxActive() {
		t.Fatal("SandboxActive() should be true")
	}
	pt, ps, ro, rw := rec.Sandbox()
	if !pt || ps != "yes" {
		t.Errorf("Sandbox basics = (pt=%v, ps=%q), want (true, yes)", pt, ps)
	}
	if !equalStrings(ro, []string{"/usr/local"}) {
		t.Errorf("ro = %v", ro)
	}
	if !equalStrings(rw, []string{"/var/lib/boxed"}) {
		t.Errorf("rw = %v", rw)
	}
	// CLONE_NEWNS must be auto-implied so the runner sees a fresh
	// mount namespace; without it the bind/remount(2) operations
	// would mutate the host filesystem.
	if rec.Cloneflags()&syscall.CLONE_NEWNS == 0 {
		t.Errorf("CLONE_NEWNS not auto-implied (cloneflags=0x%x)", rec.Cloneflags())
	}
}

// TestSandboxIdleNoNamespace verifies the inverse: a service without
// any sandbox stanza neither flags the record nor forces CLONE_NEWNS.
func TestSandboxIdleNoNamespace(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "plain",
		"type = process\ncommand = /usr/bin/plain\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	svc, err := loader.LoadService("plain")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	rec := svc.Record()
	if rec.SandboxActive() {
		t.Error("SandboxActive() should be false for plain service")
	}
	if rec.Cloneflags()&syscall.CLONE_NEWNS != 0 {
		t.Errorf("CLONE_NEWNS should not be set (cloneflags=0x%x)", rec.Cloneflags())
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
