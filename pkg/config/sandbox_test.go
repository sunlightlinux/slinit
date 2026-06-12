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
	cfg := rec.Sandbox()
	if !cfg.PrivateTmp || cfg.ProtectSystem != "yes" {
		t.Errorf("Sandbox basics = (pt=%v, ps=%q), want (true, yes)",
			cfg.PrivateTmp, cfg.ProtectSystem)
	}
	if !equalStrings(cfg.ReadOnlyPaths, []string{"/usr/local"}) {
		t.Errorf("ro = %v", cfg.ReadOnlyPaths)
	}
	if !equalStrings(cfg.ReadWritePaths, []string{"/var/lib/boxed"}) {
		t.Errorf("rw = %v", cfg.ReadWritePaths)
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

// TestParseSandboxExpansion covers every #3b stanza in a single
// realistic service description.
func TestParseSandboxExpansion(t *testing.T) {
	input := `type = process
command = /usr/bin/svc
protect-home = read-only
inaccessible-paths = /opt/secret /var/secret
protect-proc = invisible
proc-subset = pid
bind-paths = /var/data /host/in:/svc/in
bind-read-only-paths = /etc/svc-config:/etc/config
temporary-filesystem = /run/svc /tmp/scratch:size=64m,mode=0700
`
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if desc.ProtectHome != "read-only" {
		t.Errorf("ProtectHome = %q", desc.ProtectHome)
	}
	if !equalStrings(desc.InaccessiblePaths, []string{"/opt/secret", "/var/secret"}) {
		t.Errorf("InaccessiblePaths = %v", desc.InaccessiblePaths)
	}
	if desc.ProtectProc != "invisible" {
		t.Errorf("ProtectProc = %q", desc.ProtectProc)
	}
	if desc.ProcSubset != "pid" {
		t.Errorf("ProcSubset = %q", desc.ProcSubset)
	}
	// Single-arg bind expands to "src:src"; src:dst is preserved.
	wantBind := []string{"/var/data:/var/data", "/host/in:/svc/in"}
	if !equalStrings(desc.BindPaths, wantBind) {
		t.Errorf("BindPaths = %v, want %v", desc.BindPaths, wantBind)
	}
	wantBindRO := []string{"/etc/svc-config:/etc/config"}
	if !equalStrings(desc.BindReadOnlyPaths, wantBindRO) {
		t.Errorf("BindReadOnlyPaths = %v, want %v", desc.BindReadOnlyPaths, wantBindRO)
	}
	// tmpfs entries preserve the optional ":options" suffix verbatim.
	wantTmpfs := []string{"/run/svc", "/tmp/scratch:size=64m,mode=0700"}
	if !equalStrings(desc.TemporaryFileSystem, wantTmpfs) {
		t.Errorf("TemporaryFileSystem = %v, want %v", desc.TemporaryFileSystem, wantTmpfs)
	}
}

// TestParseProtectHomeLevels covers the four accepted modes (with their
// synonyms) plus rejection of an unknown value.
func TestParseProtectHomeLevels(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"yes", "yes"}, {"true", "yes"},
		{"no", ""}, {"off", ""}, {"", ""},
		{"read-only", "read-only"}, {"ro", "read-only"},
		{"tmpfs", "tmpfs"},
	} {
		input := "type = process\ncommand = /bin/true\nprotect-home = " + c.in + "\n"
		desc, err := Parse(strings.NewReader(input), "svc", "test-file")
		if err != nil {
			t.Errorf("level %q: %v", c.in, err)
			continue
		}
		if desc.ProtectHome != c.want {
			t.Errorf("level %q → %q, want %q", c.in, desc.ProtectHome, c.want)
		}
	}
	bad := "type = process\ncommand = /bin/true\nprotect-home = paranoid\n"
	if _, err := Parse(strings.NewReader(bad), "svc", "test-file"); err == nil {
		t.Fatal("expected error for unknown protect-home mode")
	}
}

// TestParseBindPathsRejectsBadPath ensures src and dst are both
// validated — a traversal in dst is just as bad as in src.
func TestParseBindPathsRejectsBadPath(t *testing.T) {
	cases := []string{
		"bind-paths = relative/path\n",
		"bind-paths = /etc/../escape:/dst\n",
		"bind-paths = /src:/etc/../escape\n",
		"bind-paths = /src:relative\n",
	}
	for _, line := range cases {
		input := "type = process\ncommand = /bin/true\n" + line
		if _, err := Parse(strings.NewReader(input), "svc", "test-file"); err == nil {
			t.Errorf("expected error for %q", line)
		}
	}
}

// TestSandboxExpansionFlowsToRecord ensures the #3b fields reach the
// ServiceRecord and trigger CLONE_NEWNS via SandboxConfig.Active().
func TestSandboxExpansionFlowsToRecord(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "boxed-ext",
		"type = process\ncommand = /usr/bin/svc\n"+
			"protect-home = tmpfs\nbind-paths = /var/data\n"+
			"temporary-filesystem = /run/svc\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	svc, err := loader.LoadService("boxed-ext")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	rec := svc.Record()
	cfg := rec.Sandbox()
	if cfg.ProtectHome != "tmpfs" {
		t.Errorf("ProtectHome = %q", cfg.ProtectHome)
	}
	if !equalStrings(cfg.BindPaths, []string{"/var/data:/var/data"}) {
		t.Errorf("BindPaths = %v", cfg.BindPaths)
	}
	if !equalStrings(cfg.TemporaryFileSystem, []string{"/run/svc"}) {
		t.Errorf("TemporaryFileSystem = %v", cfg.TemporaryFileSystem)
	}
	if rec.Cloneflags()&syscall.CLONE_NEWNS == 0 {
		t.Error("CLONE_NEWNS not auto-implied for #3b sandbox")
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
