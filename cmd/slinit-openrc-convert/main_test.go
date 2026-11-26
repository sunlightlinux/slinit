package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/config"
	"github.com/sunlightlinux/slinit/pkg/service"
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
command_background=yes
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
	// start-stop-daemon writes a command_background daemon's pidfile;
	// slinit tracks the PID itself.
	if cfg.pidFile != "" {
		t.Errorf("pidFile = %q, want none for a command_background daemon", cfg.pidFile)
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
	// No supervise-daemon: OpenRC neither respawns nor reads respawn_*.
	if cfg.restart != "no" || cfg.restartDelay != "" || cfg.restartLimitCount != "" || cfg.restartLimitCounti != "" {
		t.Errorf("restart/respawn_* mapping wrong: %+v", cfg)
	}
	if !cfg.noNewPrivs {
		t.Errorf("noNewPrivs not set")
	}

	// depend mapping
	if len(cfg.depends) != 1 || cfg.depends[0] != "localmount" {
		t.Errorf("depends = %v, want [localmount]", cfg.depends)
	}
	// use net dns + after bootmisc: no script of those names beside
	// this one, so each is left out with a note rather than emitted as
	// a dependency slinit cannot load.
	if len(cfg.waitsFor) != 0 {
		t.Errorf("waits-for = %v, want empty", cfg.waitsFor)
	}
	for _, n := range []string{"use net", "use dns", "after bootmisc"} {
		found := false
		for _, w := range warns {
			found = found || (w.level == "NOTE" && strings.Contains(w.msg, n))
		}
		if !found {
			t.Errorf("no NOTE that `%s` was left out", n)
		}
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
		`unquoted`:      "unquoted",
		`"double"`:      "double",
		`'single'`:      "single",
		`"with spaces"`: "with spaces",
		`  padded  `:    "padded",
		`""`:            "",
		`"unbalanced`:   `"unbalanced`,
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
		"options = no-new-privs",
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

// slinit refuses chroot on any type but process (it would run the
// service unjailed). A converted bgprocess or wrapped script must still
// load: the chroot is left out, with a warning that says what that means.
func TestChrootOnNonProcessTypesStillLoads(t *testing.T) {
	for _, tc := range []struct {
		name, script, level string
	}{
		{"bgprocess", "#!/sbin/openrc-run\ncommand=/usr/sbin/d\npidfile=/run/d.pid\nchroot=/var/jail\n", "WARN"},
		{"scripted", "#!/sbin/openrc-run\ncommand=/usr/sbin/d\nchroot=/var/jail\nstart() {\n\t:\n}\n", "NOTE"},
	} {
		cfg := &slinitConfig{svcType: "process", restart: "yes", scriptPath: "/etc/init.d/d"}
		warns := parseOpenrcScript(cfg, tc.script, "/usr/sbin/openrc-run")
		var buf bytes.Buffer
		emitSlinitFile(&buf, cfg)
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "d"), buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		ss := service.NewServiceSet(nil)
		dl := config.NewDirLoader(ss, []string{dir})
		ss.SetLoader(dl)
		if _, err := dl.LoadService("d"); err != nil {
			t.Errorf("%s: converted file does not load: %v\n%s", tc.name, err, buf.String())
		}
		found := false
		for _, w := range warns {
			if w.level == tc.level && strings.Contains(w.msg, "chroot") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no %s about the chroot that was left out: %v", tc.name, tc.level, warns)
		}
	}
}

// loadConverted writes the converted file into a service directory and
// loads it with the real loader, which is what an operator's slinit does.
func loadConverted(t *testing.T, cfg *slinitConfig) (service.Service, string) {
	t.Helper()
	var buf bytes.Buffer
	emitSlinitFile(&buf, cfg)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "d"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	ss := service.NewServiceSet(nil)
	dl := config.NewDirLoader(ss, []string{dir})
	ss.SetLoader(dl)
	svc, err := dl.LoadService("d")
	if err != nil {
		t.Fatalf("converted file does not load: %v\n%s", err, buf.String())
	}
	return svc, buf.String()
}

// command_background=yes means the daemon stays in the foreground and
// start-stop-daemon backgrounds it and writes the pidfile itself
// (openrc-run(8)), so slinit supervises it as a process. Without it,
// start-stop-daemon expects the daemon to fork itself: with a pidfile
// that is a bgprocess. The two used to be the other way round, and a
// command_background daemon sat in STARTING until its start timed out,
// waiting for a pidfile nothing wrote.
func TestCommandBackgroundIsAForegroundDaemon(t *testing.T) {
	for _, tc := range []struct {
		name, vars string
		typ        service.ServiceType
		pidFile    bool
		warn       string
	}{
		{"command_background", "command_background=yes\npidfile=/run/d.pid\n", service.TypeProcess, false, ""},
		{"self-forking", "pidfile=/run/d.pid\n", service.TypeBGProcess, true, ""},
		{"foreground-args", "command_args_foreground=-f\npidfile=/run/d.pid\n", service.TypeProcess, false, ""},
		{"supervise-daemon", "supervisor=supervise-daemon\npidfile=/run/d.pid\n", service.TypeProcess, false, ""},
		{"no-pidfile", "", service.TypeProcess, false, "fork"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := "#!/sbin/openrc-run\ncommand=/usr/sbin/d\n" + tc.vars
			cfg := &slinitConfig{svcType: "process", restart: "yes", scriptPath: "/etc/init.d/d"}
			warns := parseOpenrcScript(cfg, script, "/usr/sbin/openrc-run")
			svc, out := loadConverted(t, cfg)
			if svc.Type() != tc.typ {
				t.Errorf("type = %v, want %v\n%s", svc.Type(), tc.typ, out)
			}
			if got := strings.Contains(out, "pid-file = /run/d.pid"); got != tc.pidFile {
				t.Errorf("pid-file emitted = %v, want %v\n%s", got, tc.pidFile, out)
			}
			if tc.warn != "" {
				found := false
				for _, w := range warns {
					found = found || (w.level == "WARN" && strings.Contains(w.msg, tc.warn))
				}
				if !found {
					t.Errorf("no WARN mentioning %q: %v", tc.warn, warns)
				}
			}
		})
	}
}

// no_new_privs is an options member in slinit, not a setting of its
// own: a converted file with `no-new-privs = yes` did not load.
func TestNoNewPrivsLoads(t *testing.T) {
	script := "#!/sbin/openrc-run\ncommand=/usr/sbin/d\ncommand_background=yes\nno_new_privs=yes\n"
	cfg := &slinitConfig{svcType: "process", restart: "yes", scriptPath: "/etc/init.d/d"}
	parseOpenrcScript(cfg, script, "/usr/sbin/openrc-run")
	_, out := loadConverted(t, cfg)
	if !strings.Contains(out, "options = no-new-privs") {
		t.Errorf("no-new-privs not emitted as an option:\n%s", out)
	}
}

// OpenRC's use and after only order against a service that is started
// anyway, and want starts one that may fail; none fails the service
// when the other is not installed, and net, logger or dns are virtual
// names other scripts provide. Mapped to waits-for, `use net` made the
// converted file fail to load. use, after and want are waits-for: — on
// 4.0, as 4.0.2 mapped use/after, pulling the service in; ordering-only
// after: is for 4.1 — each kept only when a script of that name sits
// beside this one.
func TestDependSoftReferencesLoad(t *testing.T) {
	initd := t.TempDir()
	for _, n := range []string{"bootmisc", "netmount", "localmount"} {
		os.WriteFile(filepath.Join(initd, n), []byte("#!/sbin/openrc-run\n"), 0o755)
	}
	script := "#!/sbin/openrc-run\ncommand=/usr/sbin/d\ncommand_background=yes\n" +
		"depend() {\n\tneed localmount\n\tuse net logger\n\tafter bootmisc *\n\twant netmount\n}\n"
	cfg := &slinitConfig{svcType: "process", restart: "yes", scriptPath: filepath.Join(initd, "d")}
	parseOpenrcScript(cfg, script, "/usr/sbin/openrc-run")

	var buf bytes.Buffer
	emitSlinitFile(&buf, cfg)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "d"), buf.Bytes(), 0o644)
	for _, n := range []string{"bootmisc", "netmount", "localmount"} {
		os.WriteFile(filepath.Join(dir, n), []byte("type = internal\n"), 0o644)
	}
	ss := service.NewServiceSet(nil)
	dl := config.NewDirLoader(ss, []string{dir})
	ss.SetLoader(dl)
	svc, err := dl.LoadService("d")
	if err != nil {
		t.Fatalf("converted file does not load: %v\n%s", err, buf.String())
	}
	want := map[string]service.DependencyType{
		"localmount": service.DepRegular,
		"bootmisc":   service.DepWaitsFor,
		"netmount":   service.DepWaitsFor,
	}
	for _, d := range svc.Record().Dependencies() {
		if want[d.To.Name()] != d.DepType {
			t.Errorf("dependency on %s is %v, want %v", d.To.Name(), d.DepType, want[d.To.Name()])
		}
		delete(want, d.To.Name())
	}
	if len(want) != 0 {
		t.Errorf("missing dependencies %v\n%s", want, buf.String())
	}
}

// A wrapped script hands its variables to openrc-run, which reads them
// itself; the converter used to copy them into the file anyway, as
// shell text, so upstream agetty came out as `restart-limit-interval =
// ${respawn_period:-60}` and `pid-file = /run/${RC_SVCNAME}.pid` and did
// not load, and a command_user became run-as on openrc-run itself.
func TestWrappedScriptLeavesVariablesToOpenrcRun(t *testing.T) {
	script := `#!/sbin/openrc-run
supervisor=supervise-daemon
respawn_period="${respawn_period:-60}"
respawn_max=3
respawn_delay=2
command=/sbin/agetty
command_user=nobody
directory=/var/lib/d
umask=022
stopsig=SIGHUP
no_new_privs=yes
pidfile="/run/${RC_SVCNAME}.pid"

start_pre() {
	:
}
`
	cfg := &slinitConfig{svcType: "process", restart: "yes", scriptPath: "/etc/init.d/d"}
	parseOpenrcScript(cfg, script, "/usr/sbin/openrc-run")
	svc, out := loadConverted(t, cfg)
	if svc.Type() != service.TypeScripted {
		t.Errorf("type = %v, want scripted\n%s", svc.Type(), out)
	}
	for _, k := range []string{"pid-file", "restart", "run-as", "working-dir", "umask", "term-signal", "no-new-privs"} {
		if strings.Contains(out, k) {
			t.Errorf("wrapped script emits %s, which openrc-run applies itself:\n%s", k, out)
		}
	}
}

// upstreamScripts finds OpenRC's own init.d scripts: $OPENRC_SRC, or an
// openrc checkout beside any directory above this one. Tests that sweep
// them skip when there is none; the scripts are not copied into this
// tree.
func upstreamScripts() []string {
	dirs := []string{}
	if d := os.Getenv("OPENRC_SRC"); d != "" {
		dirs = append(dirs, d)
	}
	if wd, err := os.Getwd(); err == nil {
		for d := wd; ; d = filepath.Dir(d) {
			dirs = append(dirs, filepath.Join(d, "openrc"))
			if d == filepath.Dir(d) {
				break
			}
		}
	}
	for _, d := range dirs {
		if ins, _ := filepath.Glob(filepath.Join(d, "init.d", "*.in")); len(ins) > 0 {
			return ins
		}
	}
	return nil
}

// convertUpstream converts every upstream OpenRC init.d script and
// returns name → emitted file.
func convertUpstream(t *testing.T) map[string]string {
	t.Helper()
	ins := upstreamScripts()
	if len(ins) == 0 {
		t.Skip("no OpenRC checkout found (set OPENRC_SRC)")
	}
	dir := t.TempDir()
	out := map[string]string{}
	for _, in := range ins {
		name := strings.TrimSuffix(filepath.Base(in), ".in")
		data, err := os.ReadFile(in)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg, _, err := convertScript(p, "/usr/sbin/openrc-run")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var buf bytes.Buffer
		emitSlinitFile(&buf, cfg)
		out[name] = buf.String()
	}
	return out
}

// Every script OpenRC ships must convert to a file slinit loads.
func TestUpstreamScriptsLoad(t *testing.T) {
	for name, text := range convertUpstream(t) {
		if _, err := config.Parse(strings.NewReader(text), name, name); err != nil {
			t.Errorf("%s: converted file does not load: %v\n%s", name, err, text)
		}
	}
}

// A mapped value the shell computes, or one assigned in shell logic,
// is known only when the script runs. slinit expands $VAR in command
// from its own environment at load time, so command_args="${FOO_OPTS}
// -f" silently lost FOO_OPTS, and the scan kept the textually last of
// rarpd's if/else assignments, turning it into a command with neither
// -f nor -a. Such a script must be wrapped, with a WARN.
func TestDynamicValuesAreWrapped(t *testing.T) {
	for _, tc := range []struct{ name, body, why string }{
		{"confd-var", "command=/usr/sbin/d\ncommand_args=\"${FOO_OPTS} -f\"\ncommand_background=yes\n", "command_args="},
		{"cmd-subst", "command=/usr/sbin/d\ncommand_args=\"--id $(cat /etc/id)\"\ncommand_background=yes\n", "command_args="},
		{"backtick-pidfile", "command=/usr/sbin/d\npidfile=`cat /etc/d.pidpath`\n", "pidfile="},
		{"rarpd-if-else", "command=/usr/sbin/rarpd\ncommand_args=\"-f\"\ncommand_background=YES\n" +
			"if [ -z \"$rarpd_interface\" ]; then\n\tcommand_args=\"-f -a\"\nelse\n\tcommand_args=\"-f eth0\"\nfi\n", "command_args is assigned in shell logic"},
		{"case-arm", "command=/usr/sbin/syslogd\ncase \"$RC_UNAME\" in\n\tFreeBSD)\tpidfile=/var/run/syslog.pid;;\n\t*)\t\tpidfile=/var/run/syslogd.pid;;\nesac\n", "pidfile is assigned in shell logic"},
		{"and-list", "command=/usr/sbin/d\ncommand_background=yes\n[ -n \"$X\" ] && command_args=-v\n", "command_args is assigned in shell logic"},
		{"default-expansion", "command=/usr/sbin/d\ncommand_background=yes\n: ${command_user:=nobody}\n", "command_user is assigned in shell logic"},
		{"helper-function", "command=/usr/sbin/d\ncommand_background=yes\n_setup() {\n\tcommand_args=-v\n}\n_setup\n", "command_args is assigned in shell logic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &slinitConfig{svcType: "process", restart: "yes", scriptPath: "/etc/init.d/d"}
			warns := parseOpenrcScript(cfg, "#!/sbin/openrc-run\n"+tc.body, "/usr/sbin/openrc-run")
			svc, out := loadConverted(t, cfg)
			if svc.Type() != service.TypeScripted || !strings.Contains(out, "openrc-run /etc/init.d/d start") {
				t.Errorf("not wrapped:\n%s", out)
			}
			found := false
			for _, w := range warns {
				found = found || (w.level == "WARN" && strings.Contains(w.msg, tc.why) && strings.Contains(w.msg, "wrapped"))
			}
			if !found {
				t.Errorf("no WARN saying %q forced the wrapper: %v", tc.why, warns)
			}
		})
	}
}

// Literal values outside shell logic still convert directly, even
// with logic around them that touches nothing mapped or a quoted "if".
func TestLiteralValuesStayDirect(t *testing.T) {
	script := "#!/sbin/openrc-run\ndescription=\"start if needed; done\"\n" +
		"command=/usr/sbin/d\ncommand_args=\"-f --conf /etc/d.conf\"\ncommand_background=yes\n" +
		"if [ -e /etc/x ]; then\n\textra=1\nfi\nport=\"${RC_SVCNAME#*.}\"\n"
	cfg := &slinitConfig{svcType: "process", restart: "yes", scriptPath: "/etc/init.d/d"}
	parseOpenrcScript(cfg, script, "/usr/sbin/openrc-run")
	if cfg.svcType != "process" || cfg.command != "/usr/sbin/d -f --conf /etc/d.conf" {
		t.Errorf("type %s command %q, want process running /usr/sbin/d -f --conf /etc/d.conf", cfg.svcType, cfg.command)
	}
}

// No upstream script may convert to a command slinit would expand at
// load time, losing what the shell would have put there.
func TestUpstreamCommandsAreLiteral(t *testing.T) {
	for name, text := range convertUpstream(t) {
		for _, ln := range strings.Split(text, "\n") {
			if !strings.HasPrefix(ln, "#") && strings.ContainsAny(ln, "$`") {
				t.Errorf("%s: shell expansion in the converted file: %q\n%s", name, ln, text)
			}
		}
	}
}

// A quoted value may run on across lines, and a trailing backslash
// joins the next one. The line-by-line scan saw only the first line:
// command_args="-a<newline>-b --conf /etc/bar.conf" loaded as
// ["/usr/sbin/bar", "-a"].
func TestMultiLineValues(t *testing.T) {
	want := []string{"/usr/sbin/bar", "-a", "-b", "--conf", "/etc/bar.conf"}
	for _, tc := range []struct{ name, body string }{
		{"quoted", "command_args=\"-a\n\t-b --conf /etc/bar.conf\"\n"},
		{"backslash-split-word", "command_args=\"-a -b --co\\\nnf /etc/bar.conf\"\n"},
		{"quoted-backslash", "command_args=\"-a \\\n-b --conf /etc/bar.conf\"\n"},
		{"apostrophe-in-comment", "# don't stop here\ncommand_args=\"-a\n-b --conf /etc/bar.conf\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := "#!/sbin/openrc-run\ndescription=\"Bar\n daemon\"\ncommand=/usr/sbin/bar\n" + tc.body + "command_background=yes\n"
			cfg := &slinitConfig{svcType: "process", restart: "yes", scriptPath: "/etc/init.d/d"}
			parseOpenrcScript(cfg, script, "/usr/sbin/openrc-run")
			var buf bytes.Buffer
			emitSlinitFile(&buf, cfg)
			d, err := config.Parse(strings.NewReader(buf.String()), "d", "d")
			if err != nil {
				t.Fatalf("does not load: %v\n%s", err, buf.String())
			}
			if strings.Join(d.Command, "|") != strings.Join(want, "|") {
				t.Errorf("command = %q, want %q\n%s", d.Command, want, buf.String())
			}
		})
	}
}

// A quote left open to the end of the script cannot be read as any
// value: wrap it, and say so.
func TestUnterminatedQuoteIsWrapped(t *testing.T) {
	script := "#!/sbin/openrc-run\ncommand=/usr/sbin/bar\ncommand_args=\"-a\n-b\ncommand_background=yes\n"
	cfg := &slinitConfig{svcType: "process", restart: "yes", scriptPath: "/etc/init.d/d"}
	warns := parseOpenrcScript(cfg, script, "/usr/sbin/openrc-run")
	if cfg.svcType != "scripted" {
		t.Errorf("not wrapped: %q", cfg.command)
	}
	found := false
	for _, w := range warns {
		found = found || (w.level == "WARN" && strings.Contains(w.msg, "end of the script"))
	}
	if !found {
		t.Errorf("no WARN about the unterminated quote: %v", warns)
	}
}

// depend() written on one line was not taken for a function, so its
// dependencies were dropped without a word.
func TestInlineDepend(t *testing.T) {
	initd := t.TempDir()
	for _, n := range []string{"localmount", "bootmisc", "net"} {
		os.WriteFile(filepath.Join(initd, n), []byte("#!/sbin/openrc-run\n"), 0o755)
	}
	for _, body := range []string{
		"depend() { need localmount net; after bootmisc; }\n",
		"depend() { need localmount net; after bootmisc\n}\n",
		"depend()\n{ need localmount net; after bootmisc; }\n",
	} {
		script := "#!/sbin/openrc-run\ncommand=/usr/sbin/d\ncommand_background=yes\n" + body
		cfg := &slinitConfig{svcType: "process", restart: "yes", scriptPath: filepath.Join(initd, "d")}
		parseOpenrcScript(cfg, script, "/usr/sbin/openrc-run")
		if strings.Join(cfg.depends, " ") != "localmount net" || strings.Join(cfg.waitsFor, " ") != "bootmisc" {
			t.Errorf("%q: depends %v waits-for %v, want [localmount net] [bootmisc]", body, cfg.depends, cfg.waitsFor)
		}
		if cfg.command != "/usr/sbin/d" {
			t.Errorf("%q: command = %q", body, cfg.command)
		}
	}
}

// A depend() body is shell. What the converter cannot follow — a
// conditional need, a name from a variable, a loop — is reported, not
// mapped silently.
func TestDependShellIsReported(t *testing.T) {
	script := "#!/sbin/openrc-run\ncommand=/usr/sbin/d\ncommand_background=yes\n" +
		"depend() {\n\tlocal w=\"\"\n\tif [ -e /etc/x ]; then\n\t\tneed root\n\tfi\n\twant $w\n}\n"
	cfg := &slinitConfig{svcType: "process", restart: "yes", scriptPath: "/etc/init.d/d"}
	warns := parseOpenrcScript(cfg, script, "/usr/sbin/openrc-run")
	for _, want := range []string{"`need root` sits inside shell logic", "`want $w` left out", "local w="} {
		found := false
		for _, w := range warns {
			found = found || (w.level == "WARN" && strings.Contains(w.msg, want))
		}
		if !found {
			t.Errorf("no WARN containing %q: %v", want, warns)
		}
	}
	for _, d := range append(cfg.depends, cfg.waitsFor...) {
		if strings.Contains(d, "$") {
			t.Errorf("dependency on %q emitted", d)
		}
	}
}

// OpenRC respawns a daemon only under supervise-daemon, which is also
// the only one that reads respawn_*; start-stop-daemon starts it once.
// Every converted daemon used to get restart = yes, so one OpenRC left
// dead was restarted by slinit.
func TestRestartOnlyUnderSuperviseDaemon(t *testing.T) {
	const respawn = "respawn_delay=5\nrespawn_max=3\nrespawn_period=60\n"
	for _, tc := range []struct {
		name, vars string
		restart    service.AutoRestartMode
		limits     bool
	}{
		{"start-stop-daemon", "command_background=yes\n" + respawn, service.RestartNever, false},
		{"supervise-daemon", "supervisor=supervise-daemon\n" + respawn, service.RestartAlways, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "d")
			os.WriteFile(p, []byte("#!/sbin/openrc-run\ncommand=/usr/sbin/d\n"+tc.vars), 0o755)
			cfg, _, err := convertScript(p, "/usr/sbin/openrc-run")
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			emitSlinitFile(&buf, cfg)
			d, err := config.Parse(strings.NewReader(buf.String()), "d", "d")
			if err != nil {
				t.Fatalf("does not load: %v\n%s", err, buf.String())
			}
			if d.AutoRestart != tc.restart {
				t.Errorf("restart = %v, want %v\n%s", d.AutoRestart, tc.restart, buf.String())
			}
			for _, k := range []string{"restart-delay = 5", "restart-limit-count = 3", "restart-limit-interval = 60"} {
				if strings.Contains(buf.String(), k) != tc.limits {
					t.Errorf("%s emitted = %v, want %v\n%s", k, !tc.limits, tc.limits, buf.String())
				}
			}
		})
	}
}
