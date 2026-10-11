package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func convert(t *testing.T, yaml string) (string, []warning) {
	t.Helper()
	cfg, warns, err := parseImmortalYAML(bufio.NewScanner(strings.NewReader(yaml)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var buf bytes.Buffer
	warns = append(warns, emitSlinitFile(&buf, cfg, "svc")...)
	return buf.String(), warns
}

// The example from immortal's own README, which is the shape most
// migrations will actually hand over.
func TestConvertsTheReadmeExample(t *testing.T) {
	out, _ := convert(t, `# pkg install go-www
cmd: www
cwd: /usr/ports
log:
    file: /var/log/www.log
    age: 10  # seconds
    num: 7   # int
    size: 1  # MegaBytes
wait: 1
require:
  - foo
  - bar
`)
	for _, want := range []string{
		"type = process",
		"command = www",
		"working-dir = /usr/ports",
		"start-delay = 1",
		"logfile = /var/log/www.log",
		"logfile-rotate-time = 10",
		"logfile-max-files = 7",
		"logfile-max-size = 1048576", // 1 MegaByte, slinit counts bytes
		"assert-service-started = foo",
		"assert-service-started = bar",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// require: means "must already be running, and immortal does not start
// them". depends-on would start them, which is the opposite
// instruction, so the mapping must not reach for it.
func TestRequireDoesNotBecomeDependsOn(t *testing.T) {
	out, warns := convert(t, "cmd: /bin/true\nrequire:\n  - db\n")
	if strings.Contains(out, "depends-on") {
		t.Errorf("require became depends-on, which starts the dependency:\n%s", out)
	}
	if !strings.Contains(out, "assert-service-started = db") {
		t.Errorf("require did not become a precondition:\n%s", out)
	}
	if !hasWarning(warns, "does NOT start them") {
		t.Errorf("the difference from depends-on was not explained: %+v", warns)
	}
}

// retries: -1 forever (the default), 0 once, N times.
func TestRetriesMapping(t *testing.T) {
	cases := []struct {
		yaml, want, reject string
	}{
		{"cmd: /bin/true\n", "restart = yes", "restart-limit-count"},
		{"cmd: /bin/true\nretries: -1\n", "restart = yes", "restart-limit-count"},
		{"cmd: /bin/true\nretries: 0\n", "restart = no", "restart-limit-count"},
		{"cmd: /bin/true\nretries: 3\n", "restart-limit-count = 3", ""},
	}
	for _, c := range cases {
		out, _ := convert(t, c.yaml)
		if !strings.Contains(out, c.want) {
			t.Errorf("%q: missing %q in:\n%s", c.yaml, c.want, out)
		}
		if c.reject != "" && strings.Contains(out, c.reject) {
			t.Errorf("%q: unexpected %q in:\n%s", c.yaml, c.reject, out)
		}
	}
}

// Every emitted line has to be a directive slinit accepts. Two bugs
// here produced a service file that would not load at all — an invented
// `env =` directive, and `log-timestamp = yes` where slinit wants a
// format name. Both were caught by running slinit-check on the output
// rather than reading it, so the shapes are pinned here.
func TestNoInventedDirectives(t *testing.T) {
	out, warns := convert(t, `cmd: /bin/true
env:
    PORT: 8080
log:
    file: /var/log/x.log
    timestamp: true
`)
	for _, bad := range []string{"\nenv = ", "log-timestamp = yes", "log-timestamp = true"} {
		if strings.Contains(out, bad) {
			t.Errorf("emitted %q, which slinit's parser rejects:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "log-timestamp = iso8601") {
		t.Errorf("timestamp did not become a format slinit accepts:\n%s", out)
	}
	// The variables must survive as a comment, since nothing can carry
	// them as a directive.
	if !strings.Contains(out, "#   PORT=8080") {
		t.Errorf("env values were lost entirely:\n%s", out)
	}
	if !hasWarning(warns, "env-file") {
		t.Errorf("no warning telling the operator where env goes: %+v", warns)
	}
}

// pid.child points the other way round from slinit's pid-file: immortal
// writes it, slinit reads one the daemon wrote. Mapping it would invert
// the direction.
func TestPidKeysAreReportedNotGuessed(t *testing.T) {
	out, warns := convert(t, "cmd: /bin/true\npid:\n    child: /run/x.pid\n    follow: /run/y.pid\n")
	if strings.Contains(out, "pid-file") {
		t.Errorf("pid.child became pid-file, which reverses who writes it:\n%s", out)
	}
	if !hasWarning(warns, "pid.child") || !hasWarning(warns, "pid.follow") {
		t.Errorf("pid keys were dropped without a word: %+v", warns)
	}
}

// A config with no cmd is not convertible; immortal requires one too.
func TestMissingCmdIsAnError(t *testing.T) {
	_, _, err := parseImmortalYAML(bufio.NewScanner(strings.NewReader("cwd: /tmp\n")))
	if err == nil {
		t.Fatal("a config with no cmd converted without complaint")
	}
}

// An unknown key must be reported rather than silently ignored.
func TestUnknownKeysAreReported(t *testing.T) {
	_, warns := convert(t, "cmd: /bin/true\nnonesuch: 1\n")
	if !hasWarning(warns, "nonesuch") {
		t.Errorf("an unmapped key was dropped silently: %+v", warns)
	}
}

func hasWarning(warns []warning, substr string) bool {
	for _, w := range warns {
		if strings.Contains(w.msg, substr) {
			return true
		}
	}
	return false
}

// absoluteCommand exists because immortal relies on PATH and slinit
// does not: `command = sleep 60` goes STOPPED with no PID, measured in
// the VM. Every one of immortal's nine examples has a bare command, so
// before this the conversion produced files that could not start.
func TestAbsoluteCommandResolvesBareArgv0(t *testing.T) {
	got, w := absoluteCommand("sh -c true")
	if !strings.HasPrefix(got, "/") {
		t.Errorf("argv[0] not absolute: %q", got)
	}
	if !strings.HasSuffix(got, " -c true") {
		t.Errorf("arguments lost: %q", got)
	}
	if w == nil || w.level != "NOTE" {
		t.Errorf("expected a NOTE recording the lookup, got %v", w)
	}
}

func TestAbsoluteCommandLeavesAbsoluteAlone(t *testing.T) {
	got, w := absoluteCommand("/usr/bin/nginx -g daemon off;")
	if got != "/usr/bin/nginx -g daemon off;" {
		t.Errorf("rewrote an absolute command: %q", got)
	}
	if w != nil {
		t.Errorf("unexpected note for an absolute path: %v", w)
	}
}

// Not found must keep the original and say so loudly rather than guess
// a path.
func TestAbsoluteCommandMissingBinaryWarns(t *testing.T) {
	got, w := absoluteCommand("definitely-not-a-real-binary-xyz --flag")
	if got != "definitely-not-a-real-binary-xyz --flag" {
		t.Errorf("command altered: %q", got)
	}
	if w == nil || w.level != "WARN" {
		t.Fatalf("expected a WARN, got %v", w)
	}
	if !strings.Contains(w.msg, "will not start") {
		t.Errorf("warning does not say the consequence: %q", w.msg)
	}
}

// pid.follow changes the shape of the service: slinit adopts a process
// rather than starting one, which is bgprocess + follow-pid and no
// command. Emitting both would produce a file the loader refuses.
func TestPidFollowEmitsBgprocessWithoutCommand(t *testing.T) {
	var buf bytes.Buffer
	cfg := &immortalConfig{Cmd: "/usr/bin/nginx", PidFollow: "/run/nginx.pid"}
	warns := emitSlinitFile(&buf, cfg, "nf")
	out := buf.String()

	if !strings.Contains(out, "type = bgprocess") {
		t.Errorf("expected type = bgprocess, got:\n%s", out)
	}
	if !strings.Contains(out, "follow-pid = /run/nginx.pid") {
		t.Errorf("follow-pid missing:\n%s", out)
	}
	if strings.Contains(out, "command =") {
		t.Errorf("command emitted alongside follow-pid; the loader refuses that:\n%s", out)
	}
	var sawNote bool
	for _, w := range warns {
		if strings.Contains(w.msg, "became follow-pid") {
			sawNote = true
		}
		if w.level == "WARN" && strings.Contains(w.msg, "no equivalent") {
			t.Errorf("stale warning says follow-pid has no equivalent: %q", w.msg)
		}
	}
	if !sawNote {
		t.Error("no note explaining that pid.follow became follow-pid")
	}
}
