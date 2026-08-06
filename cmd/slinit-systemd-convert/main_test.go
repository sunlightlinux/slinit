package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestParseSimpleUnit covers the happy path: a well-formed
// .service with the common [Unit] + [Service] + [Install]
// directives. Every mapped directive should land in the config
// with the right slinit name.
func TestParseSimpleUnit(t *testing.T) {
	unit := `[Unit]
Description=Example daemon
After=network.target
Requires=other.service
Wants=optional.service
ConditionPathExists=/etc/example.conf

[Service]
Type=simple
ExecStart=/usr/bin/example --config /etc/example.conf
ExecStop=/usr/bin/example --shutdown
User=nobody
Group=nogroup
WorkingDirectory=/var/lib/example
EnvironmentFile=-/etc/default/example
PIDFile=/run/example.pid
Restart=always
RestartSec=5
NoNewPrivileges=yes
UMask=022
KillSignal=SIGHUP
LimitNOFILE=4096
StandardInput=null

[Install]
WantedBy=multi-user.target
`
	cfg := &slinitConfig{svcType: "process", restart: "no", svcName: "example"}
	warns := parseSystemdUnit(cfg, unit)

	if cfg.command != "/usr/bin/example --config /etc/example.conf" {
		t.Errorf("command = %q", cfg.command)
	}
	if cfg.stopCommand != "/usr/bin/example --shutdown" {
		t.Errorf("stopCommand = %q", cfg.stopCommand)
	}
	if cfg.runAs != "nobody:nogroup" {
		t.Errorf("runAs = %q, want nobody:nogroup", cfg.runAs)
	}
	if cfg.workingDir != "/var/lib/example" {
		t.Errorf("workingDir = %q", cfg.workingDir)
	}
	if cfg.envFile != "/etc/default/example" {
		t.Errorf("envFile = %q — the leading `-` (optional marker) should be stripped", cfg.envFile)
	}
	if cfg.pidFile != "/run/example.pid" {
		t.Errorf("pidFile = %q", cfg.pidFile)
	}
	if cfg.restart != "yes" {
		t.Errorf("restart = %q, want yes (Restart=always)", cfg.restart)
	}
	if cfg.restartDelay != "5" {
		t.Errorf("restartDelay = %q — the `s` suffix should be trimmed", cfg.restartDelay)
	}
	if !cfg.noNewPrivs {
		t.Errorf("noNewPrivs not set")
	}
	if cfg.umask != "022" {
		t.Errorf("umask = %q", cfg.umask)
	}
	if cfg.termSignal != "SIGHUP" {
		t.Errorf("termSignal = %q", cfg.termSignal)
	}
	if cfg.rlimitNofile != "4096" {
		t.Errorf("rlimitNofile = %q", cfg.rlimitNofile)
	}
	if !cfg.closeStdin {
		t.Errorf("closeStdin should be true (StandardInput=null)")
	}

	// [Unit] mappings
	if len(cfg.depends) != 1 || cfg.depends[0] != "other" {
		t.Errorf("depends = %v, want [other]", cfg.depends)
	}
	wantWaits := map[string]bool{"network": true, "optional": true}
	for _, w := range cfg.waitsFor {
		delete(wantWaits, w)
	}
	if len(wantWaits) != 0 {
		t.Errorf("missing waits-for: %v (got %v)", wantWaits, cfg.waitsFor)
	}

	if len(cfg.conditions) != 1 || cfg.conditions[0].name != "condition-path-exists" {
		t.Errorf("expected one condition-path-exists; got %v", cfg.conditions)
	}

	// [Install] should generate a NOTE
	var installNote bool
	for _, w := range warns {
		if strings.Contains(w.msg, "WantedBy") {
			installNote = true
		}
	}
	if !installNote {
		t.Errorf("expected NOTE about WantedBy from [Install]")
	}
}

// TestTypeMapping covers each Type= value's slinit svcType.
func TestTypeMapping(t *testing.T) {
	cases := map[string]string{
		"simple":        "process",
		"exec":          "process",
		"forking":       "bgprocess",
		"oneshot":       "scripted",
		"notify":        "process",
		"notify-reload": "process",
	}
	for typeVal, wantSvc := range cases {
		t.Run(typeVal, func(t *testing.T) {
			cfg := &slinitConfig{svcType: "process", restart: "no"}
			parseSystemdUnit(cfg, "[Service]\nType="+typeVal+"\nExecStart=/x\n")
			if cfg.svcType != wantSvc {
				t.Errorf("Type=%s → %s, want %s", typeVal, cfg.svcType, wantSvc)
			}
		})
	}
	// oneshot should also force restart=no
	cfg := &slinitConfig{svcType: "process", restart: "yes"}
	parseSystemdUnit(cfg, "[Service]\nType=oneshot\nExecStart=/x\n")
	if cfg.restart != "no" {
		t.Errorf("Type=oneshot should force restart=no, got %q", cfg.restart)
	}
}

// TestRestartMapping — systemd's five Restart= values map onto
// slinit's three (yes/no/on-failure) with warnings on the ambiguous
// cases.
func TestRestartMapping(t *testing.T) {
	cases := map[string]string{
		"no":          "no",
		"always":      "yes",
		"on-failure":  "on-failure",
		"on-abnormal": "on-failure",
		"on-abort":    "on-failure",
		"on-success":  "yes", // unusual, warns
		"on-watchdog": "on-failure",
	}
	for restartVal, wantSlinit := range cases {
		t.Run(restartVal, func(t *testing.T) {
			cfg := &slinitConfig{svcType: "process", restart: "no"}
			parseSystemdUnit(cfg, "[Service]\nExecStart=/x\nRestart="+restartVal+"\n")
			if cfg.restart != wantSlinit {
				t.Errorf("Restart=%s → %s, want %s", restartVal, cfg.restart, wantSlinit)
			}
		})
	}
}

// TestExecStartPrefixStripping — systemd's five prefix chars all
// get stripped from the extracted command and produce a NOTE.
func TestExecStartPrefixStripping(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/usr/bin/foo", "/usr/bin/foo"},
		{"-/usr/bin/foo --arg", "/usr/bin/foo --arg"},
		{"+/usr/bin/foo", "/usr/bin/foo"},
		{"!/usr/bin/foo", "/usr/bin/foo"},
		{"-!/usr/bin/foo", "/usr/bin/foo"},
		{"@/usr/bin/foo argv0", "/usr/bin/foo argv0"},
	}
	for _, c := range cases {
		got, _ := stripExecPrefixes(c.in)
		if got != c.want {
			t.Errorf("stripExecPrefixes(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestMultipleExecStartPre — slinit takes one pre-start-command.
// Second and subsequent lines should warn without overwriting the
// first (predictable behaviour, operator can review).
func TestMultipleExecStartPre(t *testing.T) {
	unit := `[Service]
ExecStartPre=/usr/bin/prepare-first
ExecStartPre=/usr/bin/prepare-second
ExecStart=/usr/bin/main
`
	cfg := &slinitConfig{svcType: "process", restart: "no"}
	warns := parseSystemdUnit(cfg, unit)
	if cfg.preStart != "/usr/bin/prepare-first" {
		t.Errorf("preStart = %q, want the first ExecStartPre value", cfg.preStart)
	}
	var multiWarned bool
	for _, w := range warns {
		if strings.Contains(w.msg, "multiple ExecStartPre") {
			multiWarned = true
		}
	}
	if !multiWarned {
		t.Errorf("expected WARN about multiple ExecStartPre")
	}
}

// TestMultiLineValue — systemd allows `\` line continuation. The
// parser must join those before matching KEY=VALUE.
func TestMultiLineValue(t *testing.T) {
	unit := "[Service]\nExecStart=/usr/bin/foo \\\n  --flag1 \\\n  --flag2 value\n"
	cfg := &slinitConfig{svcType: "process", restart: "no"}
	parseSystemdUnit(cfg, unit)
	if !strings.Contains(cfg.command, "--flag1") || !strings.Contains(cfg.command, "--flag2 value") {
		t.Errorf("multi-line join failed, command = %q", cfg.command)
	}
}

// TestSplitTargetsStripSuffixes — systemd deps carry .service /
// .target / .socket etc. Bare names for slinit.
func TestSplitTargetsStripSuffixes(t *testing.T) {
	got := splitTargets("network.target syslog.socket dbus.service")
	want := []string{"network", "syslog", "dbus"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestTrimSec — RestartSec / TimeoutStopSec accept `5`, `5s`, `5sec`.
// Slinit takes the bare integer.
func TestTrimSec(t *testing.T) {
	cases := map[string]string{
		"5":    "5",
		"5s":   "5",
		"5sec": "5",
		"90":   "90",
	}
	for in, want := range cases {
		if got := trimSec(in); got != want {
			t.Errorf("trimSec(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestEmitSlinitFile checks output contains every directive we
// populated with the exact name slinit's parser accepts.
func TestEmitSlinitFile(t *testing.T) {
	cfg := &slinitConfig{
		svcName:      "example",
		svcType:      "process",
		command:      "/usr/bin/example",
		stopCommand:  "/usr/bin/example --stop",
		preStart:     "/usr/bin/prep",
		runAs:        "nobody:nogroup",
		envFile:      "/etc/default/example",
		workingDir:   "/var/lib/example",
		pidFile:      "/run/example.pid",
		termSignal:   "SIGTERM",
		umask:        "022",
		noNewPrivs:   true,
		closeStdin:   true,
		restart:      "on-failure",
		restartDelay: "5",
		stopTimeout:  "30",
		rlimitNofile: "4096",
		conditions:   []condDir{{"condition-path-exists", "/etc/foo"}},
		depends:      []string{"other"},
		waitsFor:     []string{"network"},
		comments:     []string{"provenance-marker"},
	}
	var buf bytes.Buffer
	emitSlinitFile(&buf, cfg)
	out := buf.String()

	want := []string{
		"# provenance-marker",
		"type = process",
		"command = /usr/bin/example",
		"stop-command = /usr/bin/example --stop",
		"pre-start-command = /usr/bin/prep",
		"run-as = nobody:nogroup",
		"env-file = /etc/default/example",
		"working-dir = /var/lib/example",
		"pid-file = /run/example.pid",
		"term-signal = SIGTERM",
		"umask = 022",
		"no-new-privs = yes",
		"close-stdin = yes",
		"restart = on-failure",
		"restart-delay = 5",
		"stop-timeout = 30",
		"rlimit-nofile = 4096",
		"condition-path-exists = /etc/foo",
		"depends-on: other",
		"waits-for: network",
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("output missing %q\n---\n%s", w, out)
		}
	}
}
