package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

const sampleLSBScript = `#!/bin/sh
### BEGIN INIT INFO
# Provides:          my-daemon
# Required-Start:    $syslog $network
# Required-Stop:     $syslog $network
# Should-Start:      ntp
# Should-Stop:
# Default-Start:     2 3 4 5
# Default-Stop:      0 1 6
# Short-Description: My example daemon
# Description:       A longer description of my daemon
#                    that spans multiple lines.
### END INIT INFO

case "$1" in
  start) echo "Starting my-daemon" ;;
  stop)  echo "Stopping my-daemon" ;;
esac
`

const scriptNoLSB = `#!/bin/sh
# No LSB headers
case "$1" in
  start) echo "start" ;;
  stop)  echo "stop" ;;
esac
`

const scriptBSDStyle = `#!/bin/sh
# PROVIDE: my_bsd_svc
# REQUIRE: LOGIN DAEMON
# KEYWORD: shutdown

. /etc/rc.subr

name="my_bsd_svc"
rcvar="my_bsd_svc_enable"
`

func writeScript(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseLSBHeaders(t *testing.T) {
	dir := t.TempDir()
	path := writeScript(t, dir, "my-daemon", sampleLSBScript)

	info, err := ParseLSBHeaders(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(info.Provides) != 1 || info.Provides[0] != "my-daemon" {
		t.Errorf("Provides = %v, want [my-daemon]", info.Provides)
	}
	if len(info.RequiredStart) != 2 {
		t.Errorf("RequiredStart = %v, want 2 entries", info.RequiredStart)
	}
	if info.RequiredStart[0] != "$syslog" || info.RequiredStart[1] != "$network" {
		t.Errorf("RequiredStart = %v, want [$syslog $network]", info.RequiredStart)
	}
	if len(info.ShouldStart) != 1 || info.ShouldStart[0] != "ntp" {
		t.Errorf("ShouldStart = %v, want [ntp]", info.ShouldStart)
	}
	if info.ShortDescription != "My example daemon" {
		t.Errorf("ShortDescription = %q, want %q", info.ShortDescription, "My example daemon")
	}
	if len(info.DefaultStart) != 4 {
		t.Errorf("DefaultStart = %v, want 4 runlevels", info.DefaultStart)
	}
}

func TestParseLSBHeaders_NoHeaders(t *testing.T) {
	dir := t.TempDir()
	path := writeScript(t, dir, "simple", scriptNoLSB)

	info, err := ParseLSBHeaders(path)
	if err != nil {
		t.Fatal(err)
	}
	// Empty info (no headers found)
	if len(info.Provides) != 0 {
		t.Errorf("expected empty Provides, got %v", info.Provides)
	}
}

func TestInitDToServiceDescription(t *testing.T) {
	dir := t.TempDir()
	path := writeScript(t, dir, "my-daemon", sampleLSBScript)

	desc, err := InitDToServiceDescription(path)
	if err != nil {
		t.Fatal(err)
	}

	if desc.Name != "my-daemon" {
		t.Errorf("Name = %q, want %q", desc.Name, "my-daemon")
	}
	if desc.Type != 3 { // TypeScripted
		t.Errorf("Type = %d, want TypeScripted (3)", desc.Type)
	}
	// Commands are wrapped in `sh -c ... ; exec <script> <action>` so
	// OpenRC /etc/rc.conf + /etc/conf.d/<name> get sourced before the
	// init.d script runs. Verify the wrapper structure and that the
	// right action ends up in the exec.
	if len(desc.Command) != 3 || desc.Command[0] != "/bin/sh" || desc.Command[1] != "-c" {
		t.Errorf("Command = %v, want [/bin/sh -c <snippet>]", desc.Command)
	} else if !strings.Contains(desc.Command[2], "'start'") {
		t.Errorf("Command snippet missing 'start': %q", desc.Command[2])
	}
	if len(desc.StopCommand) != 3 || desc.StopCommand[0] != "/bin/sh" || desc.StopCommand[1] != "-c" {
		t.Errorf("StopCommand = %v, want [/bin/sh -c <snippet>]", desc.StopCommand)
	} else if !strings.Contains(desc.StopCommand[2], "'stop'") {
		t.Errorf("StopCommand snippet missing 'stop': %q", desc.StopCommand[2])
	}
	if desc.Description != "My example daemon" {
		t.Errorf("Description = %q, want %q", desc.Description, "My example daemon")
	}

	// Check facility mapping: $syslog → syslog, $network → network
	if len(desc.DependsOn) != 2 {
		t.Fatalf("DependsOn = %v, want 2 entries", desc.DependsOn)
	}
	if desc.DependsOn[0] != "syslog" {
		t.Errorf("DependsOn[0] = %q, want %q", desc.DependsOn[0], "syslog")
	}
	if desc.DependsOn[1] != "network" {
		t.Errorf("DependsOn[1] = %q, want %q", desc.DependsOn[1], "network")
	}

	// Should-Start → waits-for
	if len(desc.WaitsFor) != 1 || desc.WaitsFor[0] != "ntp" {
		t.Errorf("WaitsFor = %v, want [ntp]", desc.WaitsFor)
	}
}

func TestInitDToServiceDescription_NoHeaders(t *testing.T) {
	dir := t.TempDir()
	path := writeScript(t, dir, "simple", scriptNoLSB)

	desc, err := InitDToServiceDescription(path)
	if err != nil {
		t.Fatal(err)
	}

	// Name falls back to filename
	if desc.Name != "simple" {
		t.Errorf("Name = %q, want %q", desc.Name, "simple")
	}
	// No deps
	if len(desc.DependsOn) != 0 {
		t.Errorf("DependsOn = %v, want empty", desc.DependsOn)
	}
}

func TestInitDToServiceDescription_NotExecutable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "noexec")
	os.WriteFile(path, []byte(sampleLSBScript), 0644) // not executable

	_, err := InitDToServiceDescription(path)
	if err == nil {
		t.Fatal("expected error for non-executable script")
	}
}

func TestIsInitDScript(t *testing.T) {
	dir := t.TempDir()

	// Valid script
	writeScript(t, dir, "valid", "#!/bin/sh\necho hi\n")
	if !IsInitDScript(filepath.Join(dir, "valid")) {
		t.Error("expected valid script to be detected")
	}

	// Not executable
	path := filepath.Join(dir, "noexec")
	os.WriteFile(path, []byte("#!/bin/sh\n"), 0644)
	if IsInitDScript(path) {
		t.Error("expected non-executable to be rejected")
	}

	// Directory
	subdir := filepath.Join(dir, "subdir")
	os.Mkdir(subdir, 0755)
	if IsInitDScript(subdir) {
		t.Error("expected directory to be rejected")
	}

	// Binary file (no shebang)
	binPath := filepath.Join(dir, "binary")
	os.WriteFile(binPath, []byte{0x7f, 0x45}, 0755)
	if IsInitDScript(binPath) {
		t.Error("expected binary to be rejected")
	}
}

func TestMapFacility(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"$syslog", "syslog"},
		{"$network", "network"},
		{"$remote_fs", "remote-fs"},
		{"$local_fs", "local-fs"},
		{"$time", "time-sync"},
		{"$all", ""},                 // skip
		{"nginx", "nginx"},           // passthrough
		{"my-service", "my-service"}, // passthrough
	}
	for _, tt := range tests {
		got := mapFacility(tt.input)
		if got != tt.want {
			t.Errorf("mapFacility(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestMultipleProvides(t *testing.T) {
	script := `#!/bin/sh
### BEGIN INIT INFO
# Provides:          main-name alias-name
# Required-Start:
# Default-Start:     2 3 4 5
# Short-Description: Multi-provides test
### END INIT INFO
`
	dir := t.TempDir()
	path := writeScript(t, dir, "multi", script)

	desc, err := InitDToServiceDescription(path)
	if err != nil {
		t.Fatal(err)
	}
	if desc.Name != "main-name" {
		t.Errorf("Name = %q, want %q", desc.Name, "main-name")
	}
	if desc.Provides != "alias-name" {
		t.Errorf("Provides = %q, want %q", desc.Provides, "alias-name")
	}
}

// TestInitDToServiceDescription_OpenRCDepend covers the compat path:
// a script with an OpenRC-shaped depend() function (no LSB block) has
// its directives translated onto ServiceDescription fields.
func TestInitDToServiceDescription_OpenRCDepend(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skipf("no /bin/sh: %v", err)
	}
	body := `#!/sbin/openrc-run
depend() {
    need localmount fsck
    use lvm
    after clock
    before bootmisc logger
    provide storage
    keyword -docker -lxc
}
start() { :; }
`
	dir := t.TempDir()
	path := writeScript(t, dir, "storage", body)

	desc, err := InitDToServiceDescription(path)
	if err != nil {
		t.Fatal(err)
	}
	// need → depends-on
	wantDep := map[string]bool{"localmount": true, "fsck": true}
	got := map[string]bool{}
	for _, d := range desc.DependsOn {
		got[d] = true
	}
	for k := range wantDep {
		if !got[k] {
			t.Errorf("DependsOn missing %q: got %v", k, desc.DependsOn)
		}
	}
	// use → waits-for
	if len(desc.WaitsFor) != 1 || desc.WaitsFor[0] != "lvm" {
		t.Errorf("WaitsFor = %v, want [lvm]", desc.WaitsFor)
	}
	// after → AfterOptional (OpenRC's `after` is advisory ordering,
	// not a hard existence dep, so it goes to the lenient list).
	if len(desc.AfterOptional) != 1 || desc.AfterOptional[0] != "clock" {
		t.Errorf("AfterOptional = %v, want [clock]", desc.AfterOptional)
	}
	// before → BeforeOptional (same rationale as `after`).
	wantBefore := map[string]bool{"bootmisc": true, "logger": true}
	gotBefore := map[string]bool{}
	for _, d := range desc.BeforeOptional {
		gotBefore[d] = true
	}
	for k := range wantBefore {
		if !gotBefore[k] {
			t.Errorf("BeforeOptional missing %q: got %v", k, desc.BeforeOptional)
		}
	}
	// provide → Provides
	if desc.Provides != "storage" {
		t.Errorf("Provides = %q, want %q", desc.Provides, "storage")
	}
}

// TestInitDToServiceDescription_LSBWinsOverOpenRC ensures the OpenRC
// parser is skipped when LSB headers already produced dependency
// information. Otherwise a script that ships both blocks would end up
// with duplicated deps.
func TestInitDToServiceDescription_LSBWinsOverOpenRC(t *testing.T) {
	body := `#!/sbin/openrc-run
### BEGIN INIT INFO
# Provides:       hybrid
# Required-Start: $syslog
### END INIT INFO
depend() {
    need localmount
}
start() { :; }
`
	dir := t.TempDir()
	path := writeScript(t, dir, "hybrid", body)

	desc, err := InitDToServiceDescription(path)
	if err != nil {
		t.Fatal(err)
	}
	// LSB provided a dep → depend() must not run.
	for _, d := range desc.DependsOn {
		if d == "localmount" {
			t.Errorf("localmount came from depend() but LSB had deps: %v", desc.DependsOn)
		}
	}
	// LSB's $syslog should be mapped.
	if len(desc.DependsOn) != 1 || desc.DependsOn[0] != "syslog" {
		t.Errorf("DependsOn = %v, want [syslog]", desc.DependsOn)
	}
}

// TestLoadDependencies_OptionalMissingSkipped covers the regression that
// blocked case 83-initd-openrc-depend: an OpenRC-flavoured init.d script
// declaring `after some-name` for a service that isn't installed used to
// fail the whole load. AfterOptional/BeforeOptional now surface the
// OpenRC "advisory" semantics — missing targets are silently dropped
// while genuine parse/filesystem errors still propagate.
func TestLoadDependencies_OptionalMissingSkipped(t *testing.T) {
	dir := t.TempDir()
	// Existing service to reference — sanity-check the optional path
	// still hooks up real deps when they resolve.
	writeServiceFile(t, dir, "peer", "type = process\ncommand = /bin/true\n")
	writeServiceFile(t, dir, "svc",
		"type = process\ncommand = /bin/true\n"+
			"before: peer\n"+
			"after: peer\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	// Baseline: strict `after = missing` must still fail.
	writeServiceFile(t, dir, "strict-bad",
		"type = process\ncommand = /bin/true\nafter: does-not-exist\n")
	if _, err := loader.LoadService("strict-bad"); err == nil {
		t.Fatalf("strict `after = does-not-exist` should fail; got nil")
	} else if !errors.Is(err, ErrServiceNotFound) {
		t.Fatalf("strict miss should wrap ErrServiceNotFound; got %v", err)
	}

	// Post-parse patch to simulate what InitDToServiceDescription
	// does for OpenRC scripts — route the missing name through the
	// optional list. loadDependencies must swallow it.
	writeServiceFile(t, dir, "lenient",
		"type = process\ncommand = /bin/true\n")
	desc, _, err := loader.findAndParse("lenient")
	if err != nil {
		t.Fatalf("findAndParse: %v", err)
	}
	desc.AfterOptional = []string{"does-not-exist"}
	desc.BeforeOptional = []string{"still-not-here"}

	svc := loader.createService("lenient", desc)
	ss.AddService(svc)
	if err := loader.loadDependencies(svc, desc, filepath.Join(dir, "lenient")); err != nil {
		t.Fatalf("loadDependencies with only optional-missing deps errored: %v", err)
	}
}

// TestInitDToServiceDescription_OpenRC_MissingAfter_EndToEnd is the
// end-to-end regression that case 83-initd-openrc-depend hits: an OpenRC
// script that references a not-installed order-only tag in `after`
// must still load through the init.d fallback rather than erroring the
// entire load.
func TestInitDToServiceDescription_OpenRC_MissingAfter_EndToEnd(t *testing.T) {
	svcDir := t.TempDir()
	initDir := t.TempDir()

	// A native slinit service that plays the `need` target — must exist.
	writeServiceFile(t, svcDir, "openrc-need",
		"type = process\ncommand = /bin/true\n")

	// The OpenRC init.d script: `need` a real service, `after` a fake tag.
	body := `#!/sbin/openrc-run
depend() {
    need openrc-need
    after totally-not-installed-tag
}
start() { :; }
`
	writeScript(t, initDir, "openrc-depend", body)

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{svcDir})
	loader.SetInitDDirs([]string{initDir})
	ss.SetLoader(loader)

	svc, err := loader.LoadService("openrc-depend")
	if err != nil {
		t.Fatalf("openrc-depend load failed on missing `after` tag: %v", err)
	}
	if svc.Name() != "openrc-depend" {
		t.Errorf("service name = %q, want openrc-depend", svc.Name())
	}
}

// TestInitDToServiceDescription_NonOpenRCScriptSkipsDependParse checks
// that a plain #!/bin/sh script without openrc-run shebang does NOT
// invoke the OpenRC parser, even if it happens to define depend().
// This guards the compat-only intent of the fallback.
func TestInitDToServiceDescription_NonOpenRCScriptSkipsDependParse(t *testing.T) {
	body := `#!/bin/sh
# Not an OpenRC script — no shebang match.
depend() {
    need dont-map-me
}
start() { :; }
`
	dir := t.TempDir()
	path := writeScript(t, dir, "plain", body)

	desc, err := InitDToServiceDescription(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(desc.DependsOn) != 0 {
		t.Errorf("DependsOn = %v, want empty (non-openrc shebang)", desc.DependsOn)
	}
	// Sanity: still discovered as an init.d script.
	if desc.Name != "plain" {
		t.Errorf("Name = %q, want %q", desc.Name, "plain")
	}
	// unused import guard: strings must be referenced somewhere in
	// case the surrounding block gets trimmed.
	_ = strings.TrimSpace
}
