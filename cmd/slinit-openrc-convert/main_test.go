package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestParseOpenrcScriptSimple covers the happy path — script only
// sets variables + a depend() block, no custom start/stop. This is
// the case where the operator gets a self-contained slinit file
// with no runtime openrc-run dependency.
func TestParseOpenrcScriptSimple(t *testing.T) {
	script := `#!/sbin/openrc-run
description="Test daemon"
command=/usr/sbin/testd
command_args="--flag value"
command_user=nobody
pidfile=/run/testd.pid
directory=/var/lib/testd
chroot=/srv/chroot
umask=022
respawn_delay=5
respawn_max=3
respawn_period=60
no_new_privs=yes

depend() {
	need localmount
	use net dns
	after bootmisc
	before shutdown
	provide testd-alias
	keyword -docker
}
`
	cfg := &slinitConfig{svcType: "process", restart: "yes", scriptPath: "/etc/init.d/testd"}
	warns := parseOpenrcScript(cfg, script, "/usr/sbin/openrc-run")

	if cfg.command != "/usr/sbin/testd --flag value" {
		t.Errorf("command = %q, want /usr/sbin/testd --flag value", cfg.command)
	}
	if cfg.runAs != "nobody" {
		t.Errorf("runAs = %q, want nobody", cfg.runAs)
	}
	if cfg.pidFile != "/run/testd.pid" {
		t.Errorf("pidFile = %q", cfg.pidFile)
	}
	if cfg.workingDir != "/var/lib/testd" {
		t.Errorf("workingDir = %q", cfg.workingDir)
	}
	if cfg.chroot != "/srv/chroot" {
		t.Errorf("chroot = %q", cfg.chroot)
	}
	if cfg.umask != "022" {
		t.Errorf("umask = %q", cfg.umask)
	}
	if cfg.restartDelay != "5" || cfg.restartLimitCount != "3" || cfg.restartLimitCounti != "60" {
		t.Errorf("respawn_* mapping wrong: %+v", cfg)
	}
	if !cfg.noNewPrivs {
		t.Errorf("noNewPrivs not set")
	}

	// depend mapping
	if len(cfg.depends) != 1 || cfg.depends[0] != "localmount" {
		t.Errorf("depends = %v, want [localmount]", cfg.depends)
	}
	// use net dns + after bootmisc → all waits-for
	wantWaits := map[string]bool{"net": true, "dns": true, "bootmisc": true}
	for _, w := range cfg.waitsFor {
		if !wantWaits[w] {
			t.Errorf("unexpected waits-for: %s", w)
		}
		delete(wantWaits, w)
	}
	if len(wantWaits) != 0 {
		t.Errorf("missing waits-for: %v", wantWaits)
	}

	// before, provide, keyword should each generate warnings/notes
	saw := map[string]bool{}
	for _, w := range warns {
		if strings.Contains(w.msg, "before") {
			saw["before"] = true
		}
		if strings.Contains(w.msg, "provide") {
			saw["provide"] = true
		}
		if strings.Contains(w.msg, "keyword") {
			saw["keyword"] = true
		}
	}
	if !saw["before"] || !saw["provide"] || !saw["keyword"] {
		t.Errorf("missing depend warnings: %v", saw)
	}

	// No custom functions → command should be direct, not the wrapper.
	if strings.Contains(cfg.command, "openrc-run") {
		t.Errorf("simple script shouldn't be wrapped: %q", cfg.command)
	}
	if cfg.svcType != "process" {
		t.Errorf("simple script svcType = %q, want process", cfg.svcType)
	}
}

// TestParseOpenrcScriptCustomFuncs covers the fallback: any custom
// start/stop function forces the wrapper path so the original
// script's ebegin/einfo/start-stop-daemon logic keeps working.
func TestParseOpenrcScriptCustomFuncs(t *testing.T) {
	script := `#!/sbin/openrc-run
description="Complex service"
command=/usr/sbin/foo
pidfile=/run/foo.pid

depend() {
	need localmount
}

start_pre() {
	echo "before start"
}

start() {
	ebegin "Starting foo"
	start-stop-daemon --exec /usr/sbin/foo
	eend $?
}
`
	cfg := &slinitConfig{svcType: "process", restart: "yes", scriptPath: "/etc/init.d/foo"}
	warns := parseOpenrcScript(cfg, script, "/usr/sbin/openrc-run")

	if !strings.Contains(cfg.command, "openrc-run") {
		t.Errorf("expected wrapper in command, got %q", cfg.command)
	}
	if !strings.Contains(cfg.command, "/etc/init.d/foo start") {
		t.Errorf("expected `<script> start` in command, got %q", cfg.command)
	}
	if !strings.Contains(cfg.stopCommand, "openrc-run") ||
		!strings.Contains(cfg.stopCommand, "/etc/init.d/foo stop") {
		t.Errorf("expected wrapper in stop-command, got %q", cfg.stopCommand)
	}
	if cfg.svcType != "scripted" {
		t.Errorf("svcType = %q, want scripted", cfg.svcType)
	}

	// Note should mention the wrapped functions.
	var noteFound bool
	for _, w := range warns {
		if strings.Contains(w.msg, "start_pre") && strings.Contains(w.msg, "start") {
			noteFound = true
		}
	}
	if !noteFound {
		t.Errorf("expected NOTE listing wrapped funcs; got %v", warns)
	}
}

// TestParseOpenrcVariableUnrecognizedWarn — vars slinit can't map
// directly (extra_commands, supervisor, capabilities, etc.) should
// generate WARNs so the operator knows what needs manual attention.
func TestParseOpenrcVariableUnrecognizedWarn(t *testing.T) {
	script := `#!/sbin/openrc-run
supervisor=supervise-daemon
capabilities="^cap_net_bind_service"
extra_started_commands="flush reload"
required_files="/etc/foo.conf"
output_log=/var/log/foo.log
command=/usr/sbin/foo
`
	cfg := &slinitConfig{svcType: "process", restart: "yes"}
	warns := parseOpenrcScript(cfg, script, "/usr/sbin/openrc-run")

	need := []string{"supervisor", "capabilities", "extra_started_commands", "required_files", "output_log"}
	for _, k := range need {
		var found bool
		for _, w := range warns {
			if strings.Contains(w.msg, k) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected warning for %s; warns=%v", k, warns)
		}
	}
}

// TestStripQuotes — bash-style assignment values often wrap in "..."
// or '...'. Strip once from each end; leave escaped/embedded quotes
// alone (would need a full shell parser we don't have).
func TestStripQuotes(t *testing.T) {
	cases := map[string]string{
		`unquoted`:     "unquoted",
		`"double"`:     "double",
		`'single'`:     "single",
		`"with spaces"`: "with spaces",
		`  padded  `:   "padded",
		`""`:           "",
		`"unbalanced`:  `"unbalanced`,
	}
	for in, want := range cases {
		if got := stripQuotes(in); got != want {
			t.Errorf("stripQuotes(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestEmitSlinitFile checks that every directive we set gets written
// and uses the exact name slinit's parser accepts.
func TestEmitSlinitFile(t *testing.T) {
	cfg := &slinitConfig{
		svcName:      "testd",
		svcType:      "process",
		command:      "/usr/sbin/testd --flag",
		runAs:        "nobody",
		envFile:      "/etc/conf.d/testd",
		workingDir:   "/var/lib/testd",
		chroot:       "/srv/chroot",
		pidFile:      "/run/testd.pid",
		termSignal:   "SIGHUP",
		umask:        "022",
		noNewPrivs:   true,
		restart:      "yes",
		restartDelay: "5",
		depends:      []string{"localmount"},
		waitsFor:     []string{"net", "dns"},
		comments:     []string{"provenance-marker"},
	}
	var buf bytes.Buffer
	emitSlinitFile(&buf, cfg)
	out := buf.String()

	want := []string{
		"# provenance-marker",
		"type = process",
		"command = /usr/sbin/testd --flag",
		"run-as = nobody",
		"env-file = /etc/conf.d/testd",
		"working-dir = /var/lib/testd",
		"chroot = /srv/chroot",
		"pid-file = /run/testd.pid",
		"term-signal = SIGHUP",
		"umask = 022",
		"no-new-privs = yes",
		"restart = yes",
		"restart-delay = 5",
		"depends-on: localmount",
		"waits-for: net",
		"waits-for: dns",
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("output missing %q\n---\n%s", w, out)
		}
	}
}
