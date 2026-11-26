package config

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// loadSystemdDirect loads unit the way PID 1 does when no native
// description exists: through the DirLoader's systemd fallback. It
// returns the description and every conversion warning, "LEVEL: msg".
func loadSystemdDirect(t *testing.T, unit string) (*ServiceDescription, []string) {
	t.Helper()
	root := t.TempDir()
	units := filepath.Join(root, "systemd")
	writeUnit(t, units, "probe.service", unit)
	var got []string
	OnSystemdUnitWarning = func(svc, path, level, msg string) {
		got = append(got, level+": "+msg)
	}
	t.Cleanup(func() { OnSystemdUnitWarning = nil })
	dl := NewDirLoader(service.NewServiceSet(&testConsumerLogger{}), []string{filepath.Join(root, "slinit.d")})
	dl.SetSystemdDirs([]string{units})
	desc, _, err := dl.findAndParse("probe")
	if err != nil {
		t.Fatal(err)
	}
	return desc, got
}

// argvIs compares an argv exactly, empty words and all.
func argvIs(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s = %q, want %q", what, got, want)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s = %q, want %q", what, got, want)
			return
		}
	}
}

// systemd skips a comment line inside a backslash continuation
// (conf-parser.c checks for a comment before it looks at the open
// continuation). Joining it in ran the service with "#", "--debug" and
// "--insecure" for arguments: the line an administrator had commented
// out, switched back on.
func TestSystemdContinuationSkipsComments(t *testing.T) {
	unit := "[Service]\n" +
		"ExecStart=/usr/bin/foo \\\n" +
		"#  --debug --insecure \\\n" +
		"   ; --also-commented \\\n" +
		"  --bar\n" +
		// A comment ending in a backslash does not continue into the
		// next line, which is a key of its own.
		"# old: ExecStop=/bin/false \\\n" +
		"ExecStop=/usr/bin/foo-stop\n" +
		// An even run of backslashes is an escaped backslash, not a
		// continuation; the next line stands alone.
		"ExecStartPre=/usr/bin/pre a\\\\\n" +
		"ExecStartPost=/usr/bin/post\n"
	want := []string{"/usr/bin/foo", "--bar"}
	for name, desc := range map[string]*ServiceDescription{
		"converted": parseEmitted(t, unit),
		"direct":    func() *ServiceDescription { d, _ := loadSystemdDirect(t, unit); return d }(),
	} {
		argvIs(t, name+": command", desc.Command, want)
		argvIs(t, name+": stop-command", desc.StopCommand, []string{"/usr/bin/foo-stop"})
		argvIs(t, name+": pre-start-command", desc.PreStartCommand, []string{"/usr/bin/pre", `a\`})
		argvIs(t, name+": post-start-command", desc.PostStartCommand, []string{"/usr/bin/post"})
	}
}

func TestJoinSystemdLines(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"A=1 \\\n  2\n", []string{"A=1    2"}},
		{"A=1 \\\n# c\n2\n", []string{"A=1  2"}},
		{"A=1 \\\n\nB=2\n", []string{"A=1  ", "B=2"}},
		// A blank after the backslash: no continuation, as in systemd.
		{"A=1 \\ \nB=2\n", []string{"A=1 \\ ", "B=2"}},
		{"A=1\\\\\nB=2\n", []string{"A=1\\\\", "B=2"}},
		{"A=1\\\\\\\nB\n", []string{"A=1\\\\ B"}},
		// Still open at the end of the file.
		{"A=1 \\\n", []string{"A=1  "}},
	}
	for _, c := range cases {
		got := joinSystemdLines(c.in)
		if strings.Join(got, "|") != strings.Join(c.want, "|") || len(got) != len(c.want) {
			t.Errorf("joinSystemdLines(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TimeoutStartSec= was a note only, hidden without --verbose, so every
// converted unit got slinit's 60 s start-timeout: a oneshot, which
// systemd gives no start timeout at all, and a unit asking for
// infinity, were interrupted after a minute. TimeoutSec= sets both
// directions, as in systemd.
func TestSystemdStartTimeout(t *testing.T) {
	cases := []struct {
		name, unit  string
		start, stop time.Duration
	}{
		{"oneshot-default", "[Service]\nType=oneshot\nExecStart=/usr/bin/fsck-all\n", 0, service.DefaultStopTimeout},
		{"oneshot-set", "[Service]\nType=oneshot\nTimeoutStartSec=10\nExecStart=/usr/bin/x\n", 10 * time.Second, service.DefaultStopTimeout},
		// Type= after the timeout: the default depends on the type read
		// at the end, not where the line is.
		{"oneshot-timeoutsec", "[Service]\nTimeoutSec=30\nType=oneshot\nExecStart=/usr/bin/x\n", 30 * time.Second, 30 * time.Second},
		{"simple-infinity", "[Service]\nExecStart=/usr/bin/x\nTimeoutStartSec=infinity\n", 0, service.DefaultStopTimeout},
		// systemd reads 0 as infinity (parse_sec_fix_0); so does slinit.
		{"simple-zero", "[Service]\nExecStart=/usr/bin/x\nTimeoutStartSec=0\n", 0, service.DefaultStopTimeout},
		{"simple-span", "[Service]\nExecStart=/usr/bin/x\nTimeoutStartSec=5min 30s\n", 330 * time.Second, service.DefaultStopTimeout},
		{"simple-default", "[Service]\nExecStart=/usr/bin/x\n", service.DefaultStartTimeout, service.DefaultStopTimeout},
		// Later lines win, whichever key they are.
		{"timeoutsec-then-stop", "[Service]\nExecStart=/usr/bin/x\nTimeoutSec=20\nTimeoutStopSec=7\n", 20 * time.Second, 7 * time.Second},
		{"start-then-timeoutsec", "[Service]\nExecStart=/usr/bin/x\nTimeoutStartSec=3\nTimeoutSec=infinity\n", 0, 0},
		// A value that is no time span, or none, systemd logs and
		// ignores: the setting before it, or the default, stays, and a
		// oneshot keeps no start timeout. It used to fail the unit.
		{"start-garbage", "[Service]\nExecStart=/usr/bin/x\nTimeoutStartSec=garbage\n", service.DefaultStartTimeout, service.DefaultStopTimeout},
		{"start-negative", "[Service]\nExecStart=/usr/bin/x\nTimeoutStartSec=4\nTimeoutStartSec=-1\n", 4 * time.Second, service.DefaultStopTimeout},
		{"oneshot-start-empty", "[Service]\nType=oneshot\nExecStart=/usr/bin/x\nTimeoutStartSec=\n", 0, service.DefaultStopTimeout},
		{"oneshot-timeoutsec-bogus", "[Service]\nType=oneshot\nExecStart=/usr/bin/x\nTimeoutSec=bogus\n", 0, service.DefaultStopTimeout},
		{"timeoutsec-empty", "[Service]\nExecStart=/usr/bin/x\nTimeoutSec=\n", service.DefaultStartTimeout, service.DefaultStopTimeout},
		{"stop-bogus", "[Service]\nExecStart=/usr/bin/x\nTimeoutStopSec=9\nTimeoutStopSec=bogus\n", service.DefaultStartTimeout, 9 * time.Second},
		{"stop-unit-garbage", "[Service]\nExecStart=/usr/bin/x\nTimeoutStopSec=5x\n", service.DefaultStartTimeout, service.DefaultStopTimeout},
	}
	ignoredValue := map[string]bool{"start-garbage": true, "start-negative": true, "oneshot-start-empty": true,
		"oneshot-timeoutsec-bogus": true, "timeoutsec-empty": true, "stop-bogus": true, "stop-unit-garbage": true}
	for _, c := range cases {
		direct, got := loadSystemdDirect(t, c.unit)
		for name, d := range map[string]*ServiceDescription{"converted": parseEmitted(t, c.unit), "direct": direct} {
			if d.StartTimeout != c.start || d.StopTimeout != c.stop {
				t.Errorf("%s %s: start-timeout = %v, stop-timeout = %v; want %v, %v",
					c.name, name, d.StartTimeout, d.StopTimeout, c.start, c.stop)
			}
		}
		ignored := false
		for _, g := range got {
			ignored = ignored || strings.HasPrefix(g, "WARN") && strings.Contains(g, "is no time span — ignored")
		}
		if want := ignoredValue[c.name]; ignored != want {
			t.Errorf("%s: WARN that the value is ignored = %v, want %v: %v", c.name, ignored, want, got)
		}
	}
}

// An Exec*= value used to be copied as written and split by slinit's
// command parser: "" vanished, \t became t, \x41 became x41, and a lone
// ";" (systemd's separator between commands) became an argument. It is
// split as systemd's config_parse_exec splits it now, and written back
// so that slinit's parser yields the same argv.
func TestSystemdExecWordsAsSystemd(t *testing.T) {
	unit := `[Service]
ExecStart=/usr/bin/foo "" "a\tb" 'x y' \x41 "q\"uote" back\\slash \; end\s1 %n "\x25n"
ExecStartPre=/usr/bin/a 1 ; /usr/bin/b 2
ExecStartPost=/usr/bin/post "a\nb"
ExecStop=/usr/bin/stop it ; /usr/bin/stop2 ""
`
	direct, got := loadSystemdDirect(t, unit)
	for name, d := range map[string]*ServiceDescription{"converted": parseEmitted(t, unit), "direct": direct} {
		argvIs(t, name+": command", d.Command, []string{"/usr/bin/foo", "", "a\tb", "x y", "A", `q"uote`, `back\slash`,
			";", "end 1", "probe.service", "probe.service"})
		argvIs(t, name+": pre-start-command", d.PreStartCommand, []string{"/usr/bin/a", "1"})
		// A newline cannot be written in a slinit value: dropped, warned.
		argvIs(t, name+": post-start-command", d.PostStartCommand, nil)
		// One stop-command: the last, as for ExecStart.
		argvIs(t, name+": stop-command", d.StopCommand, []string{"/usr/bin/stop2", ""})
	}
	for _, want := range []string{"multiple ExecStartPre", "/usr/bin/b", "newline", "multiple ExecStop"} {
		found := false
		for _, g := range got {
			found = found || (strings.HasPrefix(g, "WARN") && strings.Contains(g, want))
		}
		if !found {
			t.Errorf("no WARN mentioning %q: %v", want, got)
		}
	}
}

func TestExtractSystemdWord(t *testing.T) {
	cases := []struct {
		in    string
		words []string
		err   bool
	}{
		{`a "b c" d`, []string{"a", "b c", "d"}, false},
		{`a"b c"d`, []string{"ab cd"}, false},
		{`'a\'b'`, []string{"a'b"}, false},
		{`"" ''`, []string{"", ""}, false},
		{`é\U0001F600\101`, []string{"é😀A"}, false},
		{`\x00`, []string{`\x00`}, false}, // NUL refused, kept as written
		{`\q`, []string{`\q`}, false},     // unknown escape, kept
		{`tail\`, []string{`tail\`}, false},
		{`"open`, nil, true},
		{`"open\`, nil, true},
	}
	for _, c := range cases {
		var words []string
		p := c.in
		var err error
		for {
			var w string
			var found bool
			w, p, found, _, err = extractSystemdWordAndWarn("ExecStart", p)
			if err != nil || !found {
				break
			}
			words = append(words, w)
		}
		if (err != nil) != c.err || (!c.err && strings.Join(words, "|") != strings.Join(c.words, "|")) || len(words) != len(c.words) {
			t.Errorf("%q: words %q err %v, want %q err %v", c.in, words, err, c.words, c.err)
		}
	}
}

// A line systemd refuses with ENOEXEC fails the unit; with "-" it is
// ignored instead. An unreadable first word ignores the line.
func TestParseExecLineRefusals(t *testing.T) {
	cfg := &SystemdConfig{svcName: "t"}
	if _, _, err := parseExecLine(cfg, "ExecStart", `/bin/x "open`); err == nil {
		t.Error("unbalanced quote after the path: want the unit refused")
	}
	if cmds, _, err := parseExecLine(cfg, "ExecStart", `-/bin/x "open`); err != nil || len(cmds) != 0 {
		t.Errorf("with -: want the line ignored, got %v, %v", cmds, err)
	}
	if cmds, w, err := parseExecLine(cfg, "ExecStart", `"/bin/x`); err != nil || len(cmds) != 0 || len(w) == 0 {
		t.Errorf("bad first word: want the line ignored with a warning, got %v, %v, %v", cmds, w, err)
	}
	if _, _, err := parseExecLine(cfg, "ExecStart", `@/bin/x`); err == nil {
		t.Error("@ with no argv[0]: want the unit refused")
	}
	// A leading ";" is skipped; ";;" is a word, not a separator.
	cmds, _, err := parseExecLine(cfg, "ExecStartPre", `; /bin/a ;; /bin/b`)
	if err != nil || len(cmds) != 1 || strings.Join(cmds[0].cmd.argv, "|") != "/bin/a|;;|/bin/b" {
		t.Errorf("separators: %+v, %v", cmds, err)
	}
	cmds, _, err = parseExecLine(cfg, "ExecStartPre", `/bin/a ; /bin/b x;`)
	if err != nil || len(cmds) != 2 || strings.Join(cmds[0].cmd.argv, "|") != "/bin/a" ||
		strings.Join(cmds[1].cmd.argv, "|") != "/bin/b|x;" {
		t.Errorf("two commands: %+v, %v", cmds, err)
	}
}

// The Exec prefixes used to be stripped with a note that only --verbose
// shows. "-" on ExecStartPre= is the common one, and stripped it made a
// pre-start that may fail (ExecStartPre=-/bin/mkdir ...) fail the whole
// start. ":" let slinit expand what the unit asked to keep literal, and
// "|" was left in the path, which then did not exist.
func TestSystemdExecPrefixes(t *testing.T) {
	unit := `[Service]
Type=oneshot
User=nobody
ExecStartPre=-/nonexistent/prog "a b"
ExecStart=-/bin/sh -c "exit 3"
ExecStartPost=+/bin/true
ExecStop=:/bin/echo $HOME ${X} $$
`
	direct, got := loadSystemdDirect(t, unit)
	for name, d := range map[string]*ServiceDescription{"converted": parseEmitted(t, unit), "direct": direct} {
		for what, argv := range map[string][]string{"pre-start-command": d.PreStartCommand, "command": d.Command} {
			if len(argv) == 0 {
				t.Errorf("%s: %s missing", name, what)
				continue
			}
			if err := exec.Command(argv[0], argv[1:]...).Run(); err != nil {
				t.Errorf("%s: %s %q failed (%v); \"-\" asks for its failure to be ignored", name, what, argv, err)
			}
		}
		argvIs(t, name+": pre-start-command", d.PreStartCommand,
			[]string{"/bin/sh", "-c", `"$@" || :`, "sh", "/nonexistent/prog", "a b"})
		// ":": no variable is expanded, $$ stays $$.
		argvIs(t, name+": stop-command", d.StopCommand, []string{"/bin/echo", "$HOME", "${X}", "$$"})
	}
	var preWarned, postWarned bool
	for _, g := range got {
		if strings.HasPrefix(g, "WARN") && strings.Contains(g, "ExecStartPre= runs as nobody") {
			preWarned = true
		}
		if strings.Contains(g, "ExecStartPost= runs as") {
			postWarned = true
		}
	}
	if !preWarned {
		t.Errorf("no WARN that ExecStartPre= runs as root, not User=: %v", got)
	}
	if postWarned {
		t.Errorf("ExecStartPost= has \"+\": systemd runs it privileged too, nothing to warn about: %v", got)
	}

	// "|": the rest runs as one /bin/sh -c script; "@|" a login shell.
	d, _ := loadSystemdDirect(t, "[Service]\nExecStart=@|echo \"a b\" $$HOME\n")
	argvIs(t, "|: command", d.Command, []string{"/bin/sh", "-c", "echo a b $HOME"})
	// exec-invoke.c: "-" and the shell's path, not "-sh".
	if d.Argv0 != "-/bin/sh" {
		t.Errorf("@|: command-argv0 = %q, want -/bin/sh", d.Argv0)
	}

	// What slinit cannot carry is a WARN, which prints without --verbose.
	for _, c := range []struct{ unit, want string }{
		{"[Service]\nExecStart=-/usr/bin/daemon\n", `"-" (exit failure counts as success) not carried`},
		{"[Service]\nExecStart=+/usr/bin/daemon\nUser=x\n", `"+" (full privileges, no sandbox) not carried`},
		{"[Service]\nExecStart=!/usr/bin/daemon\nUser=x\n", `"!" (sandbox, but no User=/Group=) not carried`},
		{"[Service]\nExecStart=/usr/bin/daemon\nExecStartPre=!/usr/bin/pre\n", `"!" (sandbox, but no User=/Group=) not carried`},
	} {
		d, got := loadSystemdDirect(t, c.unit)
		found := false
		for _, g := range got {
			found = found || (strings.HasPrefix(g, "WARN") && strings.Contains(g, c.want))
		}
		if !found {
			t.Errorf("%q: no WARN %q: %v", c.unit, c.want, got)
		}
		if d.Command[0] != "/usr/bin/daemon" {
			t.Errorf("%q: command = %q", c.unit, d.Command)
		}
	}
}
