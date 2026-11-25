package config

import (
	"bytes"
	"fmt"
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

// The rest of the seccomp family, which used to be dropped: three with a
// "no slinit equivalent" note although slinit has each, three with a
// "not recognised" warning. A unit that asked for them ran without them.
func TestSystemdSeccompFamilyIsTranslated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.service")
	os.WriteFile(path, []byte(`[Service]
ExecStart=/bin/true
RestrictSUIDSGID=yes
ProtectKernelLogs=true
ProtectHostname=yes
SystemCallArchitectures=native
SystemCallErrorNumber=EAFNOSUPPORT
SystemCallLog=@privileged
IOUringFilter=~socket,connect
`), 0o644)
	cfg, warns, err := ConvertSystemdUnit(path)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	var buf bytes.Buffer
	EmitSlinitFile(&buf, cfg)
	out := buf.String()
	for _, want := range []string{
		"restrict-suidsgid = yes",
		"protect-kernel-logs = yes",
		"protect-hostname = yes",
		"system-call-architectures = native",
		"system-call-error-number = EAFNOSUPPORT",
		"system-call-log = @privileged",
		"io-uring-filter = ~socket,connect",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in emitted file:\n%s", want, out)
		}
	}
	for _, w := range warns {
		if strings.Contains(w.Msg, "no slinit equivalent") || strings.Contains(w.Msg, "not recognised") {
			t.Errorf("dropped a directive slinit implements: %s", w.Msg)
		}
	}
	desc, err := Parse(bytes.NewReader(buf.Bytes()), "s", path)
	if err != nil {
		t.Fatalf("output does not parse: %v\n%s", err, out)
	}
	if !desc.RestrictSUIDSGID || len(desc.IOUringFilter) != 1 || desc.SystemCallErrorNumber != "EAFNOSUPPORT" {
		t.Errorf("description lost settings: suid=%v io_uring=%q errno=%q",
			desc.RestrictSUIDSGID, desc.IOUringFilter, desc.SystemCallErrorNumber)
	}

	// systemd's ProtectHostname=private has no slinit counterpart.
	// Left out, the service could set the host's hostname; it becomes
	// protect-hostname = yes, which is stricter, and says so.
	os.WriteFile(path, []byte("[Service]\nExecStart=/bin/true\nProtectHostname=private\n"), 0o644)
	cfg, warns, _ = ConvertSystemdUnit(path)
	buf.Reset()
	EmitSlinitFile(&buf, cfg)
	if !strings.Contains(buf.String(), "protect-hostname = yes") {
		t.Errorf("ProtectHostname=private not made protect-hostname = yes:\n%s", buf.String())
	}
	if len(warns) == 0 {
		t.Error("ProtectHostname=private must warn")
	}
}

// A deny-list of address families and a list of namespace types were
// dropped with a warning, so the service ran with no restriction at all.
// The deny-list becomes the allow-list of every other family, and the
// namespace list refuses every type, which is stricter.
func TestSystemdDenyListsAreKept(t *testing.T) {
	desc, warns := convertWarns(t, `[Service]
ExecStart=/bin/true
RestrictAddressFamilies=~AF_PACKET AF_NETLINK
RestrictNamespaces=net ipc
`)
	if !desc.RestrictAFEnabled {
		t.Fatal("RestrictAddressFamilies=~ dropped")
	}
	allowed := map[string]bool{}
	for _, f := range desc.RestrictAddressFamilies {
		allowed[f] = true
	}
	if allowed["17"] || allowed["16"] || !allowed["1"] || !allowed["2"] || !allowed["10"] {
		t.Errorf("~AF_PACKET AF_NETLINK became %v; want every family but 16 and 17", desc.RestrictAddressFamilies)
	}
	if !desc.RestrictNamespaces {
		t.Error("RestrictNamespaces=net ipc dropped; want restrict-namespaces = yes")
	}
	var warned bool
	for _, w := range warns {
		warned = warned || (w.Level == "WARN" && strings.Contains(w.Msg, "RestrictNamespaces"))
	}
	if !warned {
		t.Error("RestrictNamespaces=<list> → yes must warn that it is stricter")
	}
}

// RestrictAddressFamilies= folds its assignments as systemd's
// parse_address_families does: a later line of the other kind takes
// families away, an empty one resets, none allows nothing.
func TestSystemdAddressFamiliesFold(t *testing.T) {
	for _, c := range []struct {
		unit string
		want string // "" = directive not in force
	}{
		{"RestrictAddressFamilies=AF_UNIX AF_INET\nRestrictAddressFamilies=~AF_INET", "AF_UNIX"},
		{"RestrictAddressFamilies=AF_UNIX\nRestrictAddressFamilies=AF_INET6", "AF_UNIX AF_INET6"},
		{"RestrictAddressFamilies=AF_UNIX\nRestrictAddressFamilies=", ""},
		{"RestrictAddressFamilies=none", "-"},
	} {
		desc, _ := convertWarns(t, "[Service]\nExecStart=/bin/true\n"+c.unit+"\n")
		got := strings.Join(desc.RestrictAddressFamilies, " ")
		switch {
		case c.want == "" && desc.RestrictAFEnabled:
			t.Errorf("%q: restriction in force (%q); want none", c.unit, got)
		case c.want == "-" && (!desc.RestrictAFEnabled || got != ""):
			t.Errorf("%q: enabled=%v families %q; want an empty allow-list", c.unit, desc.RestrictAFEnabled, got)
		case c.want != "" && c.want != "-" && got != c.want:
			t.Errorf("%q: families %q; want %q", c.unit, got, c.want)
		}
	}
	// A deny-list naming a family slinit does not know cannot be
	// inverted: the unit is refused.
	path := filepath.Join(t.TempDir(), "u.service")
	os.WriteFile(path, []byte("[Service]\nExecStart=/bin/true\nRestrictAddressFamilies=~AF_BOGUS\n"), 0o644)
	if _, _, err := ConvertSystemdUnit(path); err == nil {
		t.Error("~AF_BOGUS converted; want an error")
	}
}

// Confinement slinit cannot express refuses the unit instead of running
// it less confined than written: an empty CapabilityBoundingSet= kept
// every capability. PrivateDevices=yes is a WARN for now (see systemd.go).
func TestSystemdUnexpressibleConfinementRefused(t *testing.T) {
	for _, line := range []string{"CapabilityBoundingSet=", "CapabilityBoundingSet=~CAP_CHOWN\nCapabilityBoundingSet="} {
		path := filepath.Join(t.TempDir(), "u.service")
		os.WriteFile(path, []byte("[Service]\nExecStart=/bin/true\n"+line+"\n"), 0o644)
		if _, _, err := ConvertSystemdUnit(path); err == nil {
			t.Errorf("%q converted; want an error", line)
		}
		if _, err := SystemdUnitToServiceDescription(path, "u"); err == nil {
			t.Errorf("%q loaded; want an error", line)
		}
	}
	if desc, warns := convertWarns(t, "[Service]\nExecStart=/bin/true\nPrivateDevices=yes\n"); desc == nil {
		t.Error("PrivateDevices=yes refused; it is a warning for now")
	} else if !strings.Contains(fmt.Sprint(warns), "PrivateDevices") {
		t.Errorf("PrivateDevices=yes loaded without a warning: %v", warns)
	}
	// PrivateDevices=no asks for nothing.
	if desc, _ := convertWarns(t, "[Service]\nExecStart=/bin/true\nPrivateDevices=no\n"); desc == nil {
		t.Error("PrivateDevices=no refused")
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

// convertWarns is parseEmitted that also returns the conversion notes.
func convertWarns(t *testing.T, unit string) (*ServiceDescription, []SystemdWarning) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "probe.service")
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, warns, err := ConvertSystemdUnit(path)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	var buf bytes.Buffer
	EmitSlinitFile(&buf, cfg)
	desc, err := Parse(bytes.NewReader(buf.Bytes()), "probe", path)
	if err != nil {
		t.Fatalf("emitted file does not parse: %v\n--- emitted ---\n%s", err, buf.String())
	}
	return desc, warns
}

// These hardening directives have slinit equivalents but were dropped
// with a note, so a unit loaded directly ran without the confinement it
// declared: no read-only paths, no hidden paths, the host's network.
func TestSystemdSandboxPathsAreTranslated(t *testing.T) {
	there := t.TempDir()
	desc, warns := convertWarns(t, `[Service]
ExecStart=/bin/true
ReadOnlyPaths=/etc -`+there+` -/nonexistent/ro
ReadWritePaths=-/nonexistent/rw /var/lib/probe
InaccessiblePaths=-/nonexistent/hidden /boot
ProtectProc=invisible
ProcSubset=pid
BindReadOnlyPaths=/etc/hosts:/run/hosts -/nonexistent/src:/x /usr:/opt/usr:rbind
TemporaryFileSystem=/var/cache:ro
PrivateNetwork=yes
`)
	check := func(name string, got, want []string) {
		t.Helper()
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s = %q; want %q", name, got, want)
		}
	}
	check("read-only-paths", desc.ReadOnlyPaths, []string{"/etc", there})
	check("read-write-paths", desc.ReadWritePaths, []string{"/var/lib/probe"})
	check("inaccessible-paths", desc.InaccessiblePaths, []string{"/nonexistent/hidden", "/boot"})
	check("bind-read-only-paths", desc.BindReadOnlyPaths, []string{"/etc/hosts:/run/hosts", "/usr:/opt/usr"})
	check("temporary-filesystem", desc.TemporaryFileSystem, []string{"/var/cache:ro,mode=0755"})
	if desc.ProtectProc != "invisible" || desc.ProcSubset != "pid" {
		t.Errorf("protect-proc %q, proc-subset %q; want invisible, pid", desc.ProtectProc, desc.ProcSubset)
	}
	if !desc.NamespaceNet {
		t.Error("PrivateNetwork=yes did not give the service a network namespace")
	}
	for _, w := range warns {
		if w.Level == "WARN" {
			t.Errorf("unexpected warning: %s", w.Msg)
		}
	}
}

// CapabilityBoundingSet= and AmbientCapabilities= fold their assignments
// as systemd's parse_capability_set does: ~ removes from the full set.
func TestSystemdCapabilitiesAreTranslated(t *testing.T) {
	desc, _ := convertWarns(t, `[Service]
ExecStart=/bin/true
CapabilityBoundingSet=CAP_NET_BIND_SERVICE CAP_CHOWN
CapabilityBoundingSet=CAP_SETUID
AmbientCapabilities=CAP_NET_BIND_SERVICE
`)
	if got := desc.CapabilityBoundingSet; got != "cap_chown cap_setuid cap_net_bind_service" {
		t.Errorf("capability-bounding-set = %q", got)
	}
	if got := desc.Capabilities; got != "cap_net_bind_service" {
		t.Errorf("capabilities = %q", got)
	}

	desc, _ = convertWarns(t, `[Service]
ExecStart=/bin/true
CapabilityBoundingSet=~CAP_SYS_ADMIN CAP_SYS_MODULE
`)
	kept := desc.CapabilityBoundingSet
	if strings.Contains(kept, "cap_sys_admin") || strings.Contains(kept, "cap_sys_module") ||
		!strings.Contains(kept, "cap_chown") || len(strings.Fields(kept)) != 39 {
		t.Errorf("~CAP_SYS_ADMIN CAP_SYS_MODULE kept %q; want every other capability", kept)
	}
}

// The forms slinit cannot express stay unmapped, and say so.
func TestSystemdUnmappableSandboxFormsWarn(t *testing.T) {
	desc, warns := convertWarns(t, `[Service]
ExecStart=/bin/true
ReadOnlyPaths=+/etc /var/%N
BindReadOnlyPaths=/usr:/opt/usr:norbind
ProtectProc=bogus
`)
	if len(desc.ReadOnlyPaths) != 0 || len(desc.BindReadOnlyPaths) != 0 ||
		desc.ProtectProc != "" {
		t.Errorf("an unmappable form was emitted: %+v", desc)
	}
	var n int
	for _, w := range warns {
		if w.Level == "WARN" {
			n++
		}
	}
	if n != 4 {
		t.Errorf("got %d WARNs, want 4 (+path, %%specifier, norbind, bogus protect-proc): %v", n, warns)
	}
}

// ExecStart=@path argv0 args runs path with argv0 as argv[0]. Stripping
// the @ alone left argv0 in the command, so it became argv[1] and every
// argument moved one place.
func TestSystemdExecArgv0Prefix(t *testing.T) {
	desc, warns := convertWarns(t, `[Service]
ExecStart=@/usr/sbin/daemon "my daemon" --flag x
ExecStartPre=@/bin/prep prep-name -v
`)
	if got := strings.Join(desc.Command, "|"); got != "/usr/sbin/daemon|--flag|x" {
		t.Errorf("command = %q; want /usr/sbin/daemon|--flag|x", got)
	}
	if desc.Argv0 != "my daemon" {
		t.Errorf("argv0 = %q; want %q", desc.Argv0, "my daemon")
	}
	if got := strings.Join(desc.PreStartCommand, "|"); got != "/bin/prep|-v" {
		t.Errorf("pre-start-command = %q; want /bin/prep|-v", got)
	}
	warned := false
	for _, w := range warns {
		if w.Level == "WARN" && strings.Contains(w.Msg, "prep-name") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("the dropped argv0 of ExecStartPre= was not a WARN: %v", warns)
	}
}

// A scripted service takes no command-argv0, so a oneshot's @ argv[0]
// is dropped with a warning rather than silently.
func TestSystemdExecArgv0PrefixOneshot(t *testing.T) {
	desc, warns := convertWarns(t, `[Service]
ExecStart=@/bin/job job-name -x
Type=oneshot
`)
	if got := strings.Join(desc.Command, "|"); got != "/bin/job|-x" {
		t.Errorf("command = %q; want /bin/job|-x", got)
	}
	for _, w := range warns {
		if w.Level == "WARN" && strings.Contains(w.Msg, "job-name") {
			return
		}
	}
	t.Errorf("the dropped argv0 of a oneshot was not a WARN: %v", warns)
}
