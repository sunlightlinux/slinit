package config

import (
	"os"
	"path/filepath"
	"testing"
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
	if len(desc.Command) != 2 || desc.Command[1] != "start" {
		t.Errorf("Command = %v, want [<path> start]", desc.Command)
	}
	if len(desc.StopCommand) != 2 || desc.StopCommand[1] != "stop" {
		t.Errorf("StopCommand = %v, want [<path> stop]", desc.StopCommand)
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
		{"$all", ""},             // skip
		{"nginx", "nginx"},       // passthrough
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
