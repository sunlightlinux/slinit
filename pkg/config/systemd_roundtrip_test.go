package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The converter's own tests compare emitted text against expected
// strings, which is why `no-new-privs = yes` shipped: the expectation
// was written from the same misunderstanding as the code, so the two
// agreed and the parser was never asked. These tests feed the output to
// the real parser instead. Anything the converter can emit must be
// something slinit can load, and that is a contract a string comparison
// cannot check.

// parseEmitted runs a unit through the converter and then through the
// production parser, returning the description the loader would get.
func parseEmitted(t *testing.T, unit string) *ServiceDescription {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "probe.service")
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}
	cfg, _, err := ConvertSystemdUnit(path)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	var buf bytes.Buffer
	EmitSlinitFile(&buf, cfg)
	desc, err := Parse(bytes.NewReader(buf.Bytes()), "probe", path)
	if err != nil {
		t.Fatalf("emitted file does not parse: %v\n--- emitted ---\n%s", err, buf.String())
	}
	return desc
}

func TestSystemdEmittedFileParses(t *testing.T) {
	// Every directive the converter can emit, in one unit. A rejection
	// here means the converter produces files slinit refuses to load.
	unit := `[Unit]
Description=Probe daemon
After=network.target
Requires=dbus.service

[Service]
Type=simple
ExecStart=/usr/bin/probe --foreground
ExecStop=/usr/bin/probe --stop
ExecStartPre=/usr/bin/probe --check
User=probe
Group=probe
WorkingDirectory=/var/lib/probe
RootDirectory=/srv/probe
EnvironmentFile=/etc/default/probe
PIDFile=/run/probe.pid
Restart=on-failure
RestartSec=5
TimeoutStopSec=30
NoNewPrivileges=yes
UMask=0027
KillSignal=SIGQUIT
LimitNOFILE=8192
LimitCORE=0
OOMScoreAdjust=-100
Nice=5
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=read-only
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
ProtectClock=yes
RestrictRealtime=yes
RestrictNamespaces=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
RestrictAddressFamilies=AF_INET AF_UNIX
SystemCallFilter=@system-service
`
	desc := parseEmitted(t, unit)

	if len(desc.Command) == 0 {
		t.Error("command did not survive the round trip")
	}
	// Description= is a directive, not decoration: it was emitted as a
	// comment, so it survived in the file and never reached the service.
	if desc.Description != "Probe daemon" {
		t.Errorf("description = %q, want %q", desc.Description, "Probe daemon")
	}
	// User= plus Group= collapse into one run-as value.
	if desc.RunAs != "probe:probe" {
		t.Errorf("run-as = %q, want probe:probe", desc.RunAs)
	}
}

// NoNewPrivileges is the specific regression: it was emitted as a
// standalone setting, which the parser rejects as unknown, so every
// converted unit that hardened itself this way was unloadable.
func TestSystemdNoNewPrivilegesIsAnOption(t *testing.T) {
	desc := parseEmitted(t, `[Service]
ExecStart=/bin/true
NoNewPrivileges=yes
`)
	if !desc.NoNewPrivs {
		t.Error("NoNewPrivileges=yes did not reach the description; it must be emitted as `options = no-new-privs`, not a setting of its own")
	}
}

// The hardening switches slinit implements as systemd equivalents should
// be translated, not deferred to the operator with a note.
func TestSystemdHardeningIsTranslated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "h.service")
	os.WriteFile(path, []byte(`[Service]
ExecStart=/bin/true
PrivateTmp=yes
ProtectSystem=full
ProtectHome=tmpfs
SystemCallFilter=@system-service
`), 0o644)
	cfg, warns, err := ConvertSystemdUnit(path)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	var buf bytes.Buffer
	EmitSlinitFile(&buf, cfg)
	out := buf.String()
	for _, want := range []string{
		"private-tmp = yes",
		"protect-system = full",
		"protect-home = tmpfs",
		"system-call-filter = @system-service",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in emitted file:\n%s", want, out)
		}
	}
	for _, w := range warns {
		if strings.Contains(w.Msg, "check slinit-supports") {
			t.Errorf("directive deferred to the operator though slinit implements it: %s", w.Msg)
		}
	}
	if _, err := Parse(bytes.NewReader(buf.Bytes()), "h", path); err != nil {
		t.Fatalf("hardening output does not parse: %v\n%s", err, out)
	}
}

// A deny-list cannot be expressed by an allow-list directive; converting
// it anyway would invert the operator's intent, which is worse than
// declining to convert it.
func TestSystemdDenyListsAreNotSilentlyInverted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "d.service")
	os.WriteFile(path, []byte(`[Service]
ExecStart=/bin/true
RestrictAddressFamilies=~AF_PACKET
RestrictNamespaces=net ipc
`), 0o644)
	cfg, warns, err := ConvertSystemdUnit(path)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	var buf bytes.Buffer
	EmitSlinitFile(&buf, cfg)
	out := buf.String()
	if strings.Contains(out, "restrict-address-families") {
		t.Errorf("a ~ deny-list was emitted as an allow-list:\n%s", out)
	}
	if strings.Contains(out, "restrict-namespaces") {
		t.Errorf("a namespace-type list was emitted as a blanket switch:\n%s", out)
	}
	var warned int
	for _, w := range warns {
		if w.Level == "WARN" {
			warned++
		}
	}
	if warned < 2 {
		t.Errorf("both unconvertible directives should warn, got %d WARNs", warned)
	}
}

// After=network.target is in nearly every unit a distribution ships. If
// it becomes a dependency on a service named "network", the unit fails
// to load anywhere that service does not exist — which is most places
// that are not running systemd. The reference is dropped and noted.
func TestSystemdTargetDepsAreDroppedNotInvented(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.service")
	os.WriteFile(path, []byte(`[Unit]
After=network.target sysinit.target real-dep.service
Requires=basic.target another.service
[Service]
ExecStart=/bin/true
`), 0o644)
	cfg, warns, err := ConvertSystemdUnit(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range append(cfg.waitsFor, cfg.depends...) {
		switch d {
		case "network", "sysinit", "basic":
			t.Errorf("target %q was turned into a service dependency", d)
		}
	}
	// Real unit references must still come through.
	var sawReal, sawAnother bool
	for _, d := range cfg.waitsFor {
		if d == "real-dep" {
			sawReal = true
		}
	}
	for _, d := range cfg.depends {
		if d == "another" {
			sawAnother = true
		}
	}
	if !sawReal || !sawAnother {
		t.Errorf("real unit deps lost: waits-for=%v depends=%v", cfg.waitsFor, cfg.depends)
	}
	var noted int
	for _, w := range warns {
		if strings.Contains(w.Msg, "dropped") {
			noted++
		}
	}
	if noted < 2 {
		t.Errorf("dropped targets must be reported, got %d notes", noted)
	}
}
