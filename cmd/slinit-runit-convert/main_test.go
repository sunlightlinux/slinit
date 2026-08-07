package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestHasShellMetachars covers the guard that decides between an
// extracted daemon command line and the /bin/sh wrap fallback. False
// positives are cheaper (safe wrap) than false negatives (broken
// service that lost shell semantics), so the metachar set stays
// deliberately wide.
func TestHasShellMetachars(t *testing.T) {
	cases := map[string]bool{
		"daemon --flag value":       false,
		"daemon -f /etc/daemon.cfg": false,
		"daemon $VAR":               true, // variable reference
		"daemon 2>&1":               true, // redirect
		"daemon | logger":           true, // pipe
		"daemon &":                  true, // background
		"cmd `subshell`":            true, // backticks
		"cmd; other":                true, // multiple commands
		"cmd <input":                true, // input redirect
	}
	for in, want := range cases {
		if got := hasShellMetachars(in); got != want {
			t.Errorf("hasShellMetachars(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestShellFields — enough shell-quoting fidelity to peel chpst
// cleanly. Not aiming for POSIX-complete grammar; the fallback path
// catches everything real-world we can't parse.
func TestShellFields(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"daemon", []string{"daemon"}},
		{"daemon --flag value", []string{"daemon", "--flag", "value"}},
		{`chpst -u user daemon`, []string{"chpst", "-u", "user", "daemon"}},
		{`daemon "quoted arg with spaces"`, []string{"daemon", "quoted arg with spaces"}},
		{`daemon 'single quoted'`, []string{"daemon", "single quoted"}},
		{`  leading  spaces  `, []string{"leading", "spaces"}},
	}
	for _, c := range cases {
		got := shellFields(c.in)
		if !equalSlices(got, c.want) {
			t.Errorf("shellFields(%q)\n  got %v\n want %v", c.in, got, c.want)
		}
	}
}

func equalSlices(a, b []string) bool {
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

// TestParseChpstBasics covers the flag forms slinit maps
// definitively (no warnings) plus one warning case.
func TestParseChpstBasics(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    slinitConfig
		wantCmd string
	}{
		{
			name:    "just daemon",
			args:    []string{"daemon", "--flag"},
			wantCmd: "daemon --flag",
		},
		{
			name: "run-as",
			args: []string{"-u", "nobody", "daemon"},
			want: slinitConfig{runAs: "nobody"},
			wantCmd: "daemon",
		},
		{
			name: "run-as combined form",
			args: []string{"-unobody:nogroup", "daemon"},
			want: slinitConfig{runAs: "nobody:nogroup"},
			wantCmd: "daemon",
		},
		{
			name: "argv0",
			args: []string{"-b", "renamed", "daemon"},
			want: slinitConfig{argv0: "renamed"},
			wantCmd: "daemon",
		},
		{
			name: "working-dir + chroot",
			args: []string{"-C", "/var/lib/svc", "-/", "/srv/chroot", "daemon"},
			want: slinitConfig{workingDir: "/var/lib/svc", chroot: "/srv/chroot"},
			wantCmd: "daemon",
		},
		{
			name: "close fds bundle",
			args: []string{"-N", "daemon"},
			want: slinitConfig{closeStdin: true, closeStdout: true, closeStderr: true},
			wantCmd: "daemon",
		},
		{
			name: "individual close fds",
			args: []string{"-0", "-1", "-2", "daemon"},
			want: slinitConfig{closeStdin: true, closeStdout: true, closeStderr: true},
			wantCmd: "daemon",
		},
		{
			name: "new-session",
			args: []string{"-P", "daemon"},
			want: slinitConfig{newSession: true},
			wantCmd: "daemon",
		},
		{
			name: "rlimit-nofile",
			args: []string{"-o", "1024", "daemon"},
			want: slinitConfig{rlimitNofile: "1024"},
			wantCmd: "daemon",
		},
		{
			name: "lock-file",
			args: []string{"-l", "/run/svc.lock", "daemon"},
			want: slinitConfig{lockFile: "/run/svc.lock"},
			wantCmd: "daemon",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var cfg slinitConfig
			parseChpst(&cfg, c.args)
			// Command comparison — bare
			if cfg.command != c.wantCmd {
				t.Errorf("command = %q, want %q", cfg.command, c.wantCmd)
			}
			// Field comparison — copy expected fields
			c.want.command = c.wantCmd
			if !reflect.DeepEqual(cfg, c.want) {
				t.Errorf("cfg mismatch\n  got %+v\n want %+v", cfg, c.want)
			}
		})
	}
}

// TestParseChpstWarnings covers the flags that either aren't
// mappable at all (nice, PID namespaces) or map with a caveat
// (memory rlimit approximation, envdir vs env-file).
func TestParseChpstWarnings(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantWarn   string
	}{
		{"nice level", []string{"-n", "5", "daemon"}, "chpst -n 5"},
		{"envdir approximation", []string{"-e", "/etc/env", "daemon"}, "chpst -e /etc/env"},
		{"unmapped rlimit nproc", []string{"-p", "100", "daemon"}, "chpst -p 100"},
		{"unmapped rlimit fsize", []string{"-f", "1000000", "daemon"}, "chpst -f 1000000"},
		{"env-only user", []string{"-U", "nobody", "daemon"}, "chpst -U nobody"},
		{"pid namespace F", []string{"-F", "daemon"}, "chpst -F"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var cfg slinitConfig
			warns := parseChpst(&cfg, c.args)
			var found bool
			for _, w := range warns {
				if strings.Contains(w.msg, c.wantWarn) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected warning containing %q; got %v", c.wantWarn, warns)
			}
		})
	}
}

// TestAnalyzeRunScriptCases covers the run-script heuristics: exec
// extraction, chpst extraction, complex-script fallback, and the
// conf-sourcing detection.
func TestAnalyzeRunScriptCases(t *testing.T) {
	cases := []struct {
		name          string
		script        string
		wantCommand   string
		wantRunAs     string
		wantEnvFile   string
		wantFallback  bool
		wantNoteHint  string
	}{
		{
			name:        "simple exec",
			script:      "#!/bin/sh\nexec 2>&1\nexec /usr/bin/daemon",
			wantCommand: "/usr/bin/daemon",
		},
		{
			name:        "exec with args",
			script:      "#!/bin/sh\nexec daemon --config /etc/daemon.cfg",
			wantCommand: "daemon --config /etc/daemon.cfg",
		},
		{
			name:        "chpst with user",
			script:      "#!/bin/sh\nexec 2>&1\nexec chpst -u nobody daemon",
			wantCommand: "daemon",
			wantRunAs:   "nobody",
		},
		{
			name:         "shell metachars ($VAR)",
			script:       "#!/bin/sh\nexec 2>&1\nexec daemon $ARGS",
			wantFallback: true,
			wantNoteHint: "metachars",
		},
		{
			name:         "shell metachars (redirect)",
			script:       "#!/bin/sh\nexec daemon 2>/var/log/daemon.log",
			wantFallback: true,
			wantNoteHint: "metachars",
		},
		{
			name:         "setup logic before exec",
			script:       "#!/bin/sh\nwhile ! ping -c1 host; do sleep 1; done\nexec daemon",
			wantFallback: true,
			wantNoteHint: "setup logic",
		},
		{
			name:        "conf sourced",
			script:      "#!/bin/sh\n[ -r conf ] && . ./conf\nexec daemon",
			wantCommand: "daemon",
			wantEnvFile: "/tmp/dir/conf",
		},
		{
			// sv check gates the exec (`sv check X || exit 1`), so
			// dropping the check would silently start the daemon
			// without its dep ready. Fall back to /bin/sh wrap so
			// the check keeps firing, and emit a NOTE hinting at
			// the cleaner waits-for equivalent.
			name:         "sv check dep",
			script:       "#!/bin/sh\nsv check dbus\nexec daemon",
			wantFallback: true,
			wantNoteHint: "waits-for: dbus",
		},
		{
			name:        "no exec line at all",
			script:      "#!/bin/sh\ndaemon --run-inline\n",
			wantCommand: "/bin/sh /tmp/dir/run",
			wantNoteHint: "no `exec`",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &slinitConfig{runitDir: "/tmp/dir"}
			warns := analyzeRunScript(cfg, c.script)

			// wantFallback: command must be the /bin/sh wrapper.
			isFallback := strings.HasPrefix(cfg.command, "/bin/sh /tmp/dir/run")
			if c.wantFallback && !isFallback {
				t.Errorf("expected fallback wrap, got command=%q", cfg.command)
			}
			if !c.wantFallback && c.wantCommand != "" && cfg.command != c.wantCommand {
				t.Errorf("command = %q, want %q", cfg.command, c.wantCommand)
			}
			if c.wantRunAs != "" && cfg.runAs != c.wantRunAs {
				t.Errorf("runAs = %q, want %q", cfg.runAs, c.wantRunAs)
			}
			if c.wantEnvFile != "" && cfg.envFile != c.wantEnvFile {
				t.Errorf("envFile = %q, want %q", cfg.envFile, c.wantEnvFile)
			}
			if c.wantNoteHint != "" {
				var found bool
				for _, w := range warns {
					if strings.Contains(w.msg, c.wantNoteHint) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected note containing %q; got %v", c.wantNoteHint, warns)
				}
			}
		})
	}
}

// TestEmitSlinitFile verifies the output matches the directive names
// slinit's parser recognises. Directive presence, not exact byte
// layout — layout can shift, presence can't.
func TestEmitSlinitFile(t *testing.T) {
	cfg := &slinitConfig{
		svcName:       "test",
		svcType:       "process",
		command:       "/usr/bin/daemon --flag",
		runAs:         "nobody:nogroup",
		envFile:       "/etc/svc/conf",
		workingDir:    "/var/lib/svc",
		lockFile:      "/run/svc.lock",
		rlimitNofile:  "1024",
		newSession:    true,
		closeStdin:    true,
		finishCommand: "/etc/sv/svc/finish",
		readyCheckCmd: "/bin/sh /etc/sv/svc/check",
		manual:        true,
		restart:       "yes",
		restartDelay:  "1",
		waitsFor:      []string{"dbus", "syslog"},
		consumerOf:    "producer",
		comments:      []string{"generated-by-test"},
	}
	var buf bytes.Buffer
	emitSlinitFile(&buf, cfg)
	out := buf.String()

	want := []string{
		"# generated-by-test",
		"type = process",
		"command = /usr/bin/daemon --flag",
		"run-as = nobody:nogroup",
		"env-file = /etc/svc/conf",
		"working-dir = /var/lib/svc",
		"lock-file = /run/svc.lock",
		"rlimit-nofile = 1024",
		"options = new-session",
		"close-stdin = yes",
		"finish-command = /etc/sv/svc/finish",
		"ready-check-command = /bin/sh /etc/sv/svc/check",
		"manual = yes",
		"restart = yes",
		"restart-delay = 1",
		"consumer-of = producer",
		"waits-for: dbus",
		"waits-for: syslog",
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in output:\n%s", w, out)
		}
	}
}

// TestConvertServiceAuxFiles verifies that finish, check, down, and
// conf get auto-detected + wired without operator intervention. This
// is the guarantee that runit → slinit conversion needs no manual
// second pass for typical void service dirs.
func TestConvertServiceAuxFiles(t *testing.T) {
	dir := t.TempDir()
	svcDir := filepath.Join(dir, "aux-svc")
	if err := os.MkdirAll(svcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// run: simplest possible daemon launcher
	if err := os.WriteFile(filepath.Join(svcDir, "run"),
		[]byte("#!/bin/sh\nexec /usr/bin/daemon\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// finish, check, down, conf: all get auto-detected
	for _, f := range []string{"finish", "check", "conf"} {
		if err := os.WriteFile(filepath.Join(svcDir, f),
			[]byte("#!/bin/sh\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(svcDir, "down"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := convertService(svcDir)
	if err != nil {
		t.Fatal(err)
	}

	wantFinish := "/bin/sh " + filepath.Join(svcDir, "finish")
	if cfg.finishCommand != wantFinish {
		t.Errorf("finishCommand = %q, want %q", cfg.finishCommand, wantFinish)
	}
	wantCheck := "/bin/sh " + filepath.Join(svcDir, "check")
	if cfg.readyCheckCmd != wantCheck {
		t.Errorf("readyCheckCmd = %q, want %q", cfg.readyCheckCmd, wantCheck)
	}
	if !cfg.manual {
		t.Errorf("manual should be true when down file exists")
	}
	wantConf := filepath.Join(svcDir, "conf")
	if cfg.envFile != wantConf {
		t.Errorf("envFile = %q, want %q", cfg.envFile, wantConf)
	}
	// workingDir defaults to svcDir so finish's ${PWD##*-} works and
	// the wrapped run script can source ./conf relatively.
	if cfg.workingDir != svcDir {
		t.Errorf("workingDir = %q, want %q", cfg.workingDir, svcDir)
	}
}

// TestConvertServiceLogChild covers the log/run companion generation.
// The primary should stay clean; the log config should carry
// consumer-of pointing at the primary's name.
func TestConvertServiceLogChild(t *testing.T) {
	dir := t.TempDir()
	svcDir := filepath.Join(dir, "logged-svc")
	logDir := filepath.Join(svcDir, "log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svcDir, "run"),
		[]byte("#!/bin/sh\nexec /usr/bin/daemon\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "run"),
		[]byte("#!/bin/sh\nexec vlogger -t logged-svc -p daemon\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg, warns, err := convertService(svcDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.logChild == nil {
		t.Fatalf("expected logChild to be populated; warns=%v", warns)
	}
	if cfg.logChild.svcName != "logged-svc-log" {
		t.Errorf("logChild svcName = %q, want logged-svc-log", cfg.logChild.svcName)
	}
	if cfg.logChild.consumerOf != "logged-svc" {
		t.Errorf("logChild consumerOf = %q, want logged-svc", cfg.logChild.consumerOf)
	}
	if cfg.logChild.command != "vlogger -t logged-svc -p daemon" {
		t.Errorf("logChild command = %q, want extracted vlogger line", cfg.logChild.command)
	}
}

// TestConvertServiceLogChildAlsoSetsLogTypePipe — slinit rejects a
// consumer-of pointer unless the producer declares log-type = pipe,
// so the primary MUST get that directive alongside the auto-generated
// log companion. Regression guard against silently generating an
// unlint-clean pair.
func TestConvertServiceLogChildAlsoSetsLogTypePipe(t *testing.T) {
	dir := t.TempDir()
	svcDir := filepath.Join(dir, "producer")
	logDir := filepath.Join(svcDir, "log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svcDir, "run"),
		[]byte("#!/bin/sh\nexec /usr/bin/daemon\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "run"),
		[]byte("#!/bin/sh\nexec /bin/cat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := convertService(svcDir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.logTypePipe {
		t.Errorf("primary cfg.logTypePipe should be true when logChild exists")
	}
	var buf bytes.Buffer
	emitSlinitFile(&buf, cfg)
	if !strings.Contains(buf.String(), "log-type = pipe") {
		t.Errorf("primary output should contain `log-type = pipe`; got:\n%s", buf.String())
	}
}

// TestConvertServiceEnvFileMissingDropped — void run scripts guard
// their conf sourcing with `[ -r conf ]` and ship without the conf
// file when there's nothing to configure. Emitting env-file for a
// non-existent file would fail slinit-check; drop the directive
// silently instead.
func TestConvertServiceEnvFileMissingDropped(t *testing.T) {
	dir := t.TempDir()
	svcDir := filepath.Join(dir, "no-conf")
	if err := os.MkdirAll(svcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Run script references conf but the file itself doesn't exist.
	if err := os.WriteFile(filepath.Join(svcDir, "run"),
		[]byte("#!/bin/sh\n[ -r conf ] && . ./conf\nexec /usr/bin/daemon\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := convertService(svcDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.envFile != "" {
		t.Errorf("envFile = %q, want empty (file doesn't exist on disk)", cfg.envFile)
	}
}

// TestResolveBareCommand — bare command names get rewritten to their
// absolute PATH resolution. `/bin/sh` is guaranteed on any POSIX-y
// test host, so it's the reliable probe.
func TestResolveBareCommand(t *testing.T) {
	// Already absolute — untouched.
	if got := resolveBareCommand("/bin/foo --flag"); got != "/bin/foo --flag" {
		t.Errorf("absolute path modified: %q", got)
	}
	// Empty — untouched.
	if got := resolveBareCommand(""); got != "" {
		t.Errorf("empty modified: %q", got)
	}
	// Bare `sh` should resolve to /bin/sh or /usr/bin/sh.
	got := resolveBareCommand("sh -c 'foo'")
	if !strings.HasPrefix(got, "/") {
		t.Errorf("expected absolute path prefix for `sh -c ...`, got %q", got)
	}
	if !strings.HasSuffix(got, "/sh -c foo") {
		t.Errorf("suffix wrong (shellFields join may have dropped quotes ok): %q", got)
	}
	// Unknown binary — untouched.
	if got := resolveBareCommand("definitely-not-a-real-binary-xyz --arg"); got != "definitely-not-a-real-binary-xyz --arg" {
		t.Errorf("unresolvable name shouldn't be rewritten, got %q", got)
	}
}

// TestConvertServiceSvCheckAutoWaitsFor confirms `sv check DEP` in a
// run script populates waitsFor without operator intervention — the
// key 1:1 wiring this refactor was about.
func TestConvertServiceSvCheckAutoWaitsFor(t *testing.T) {
	dir := t.TempDir()
	svcDir := filepath.Join(dir, "elogind-like")
	if err := os.MkdirAll(svcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svcDir, "run"),
		[]byte("#!/bin/sh\nsv check dbus >/dev/null || exit 1\nexec /usr/libexec/elogind\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := convertService(svcDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.waitsFor) != 1 || cfg.waitsFor[0] != "dbus" {
		t.Errorf("waitsFor = %v, want [dbus]", cfg.waitsFor)
	}
	// Still falls back to wrap so the runtime sv check keeps firing.
	if !strings.HasPrefix(cfg.command, "/bin/sh ") {
		t.Errorf("command = %q, want /bin/sh wrap (sv check is gating logic)", cfg.command)
	}
}
