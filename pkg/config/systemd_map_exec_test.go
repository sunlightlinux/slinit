package config

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/process"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// exConvert runs [Service] lines through both consumers of the mapping:
// the converter's emitted file, and the description the loader builds
// from the same unit.
func exConvert(t *testing.T, body string) (string, []SystemdWarning, *ServiceDescription) {
	t.Helper()
	p := writeUnit(t, t.TempDir(), "probe.service", "[Service]\nExecStart=/bin/sleep 1\n"+body)
	cfg, warns, err := ConvertSystemdUnit(p)
	if err != nil {
		t.Fatalf("convert:\n%s\n%v", body, err)
	}
	var buf bytes.Buffer
	EmitSlinitFile(&buf, cfg)
	desc, err := SystemdUnitToServiceDescription(p, "probe")
	if err != nil {
		t.Fatalf("load:\n%s\n%v", body, err)
	}
	return buf.String(), warns, desc
}

// exLines returns the values of every emitted "key = value" line.
func exLines(out, key string) []string {
	var vals []string
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(l, key+" = "); ok {
			vals = append(vals, v)
		}
	}
	return vals
}

// exHas reports whether a warning of level holds every one of subs.
func exHas(warns []SystemdWarning, level string, subs ...string) bool {
	for _, w := range warns {
		if w.Level != level {
			continue
		}
		all := true
		for _, s := range subs {
			if !strings.Contains(w.Msg, s) {
				all = false
			}
		}
		if all {
			return true
		}
	}
	return false
}

// exCase is one unit snippet: the emitted lines expected (nil: none of
// that key), a warning expected, and a check on the parsed description.
type exCase struct {
	name  string
	body  string
	lines map[string][]string
	warn  []string // level, then substrings
	desc  func(t *testing.T, d *ServiceDescription)
}

func runExCases(t *testing.T, cases []exCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, warns, desc := exConvert(t, c.body)
			for k, want := range c.lines {
				if got := exLines(out, k); !reflect.DeepEqual(got, want) && !(len(got) == 0 && len(want) == 0) {
					t.Errorf("%s: %q, want %q\n%s", k, got, want, out)
				}
			}
			if len(c.warn) > 0 && !exHas(warns, c.warn[0], c.warn[1:]...) {
				t.Errorf("no %s holding %q in %v", c.warn[0], c.warn[1:], warns)
			}
			if c.desc != nil {
				c.desc(t, desc)
			}
		})
	}
}

func TestSystemdTTYCluster(t *testing.T) {
	runExCases(t, []exCase{
		{name: "getty", body: "StandardInput=tty\nTTYPath=/dev/tty3\nTTYReset=yes\nTTYVHangup=on\nTTYVTDisallocate=1\nTTYRows=30\nTTYColumns=100\n",
			lines: map[string][]string{"tty-path": {"/dev/tty3"}, "tty-reset": {"yes"}, "tty-vhangup": {"yes"},
				"tty-vt-disallocate": {"yes"}, "tty-rows": {"30"}, "tty-columns": {"100"}},
			warn: []string{"NOTE", "StandardInput=tty", "unmask-intr"},
			desc: func(t *testing.T, d *ServiceDescription) {
				if !d.Flags.UnmaskIntr {
					t.Error("StandardInput=tty: the tty is not made the controlling terminal (unmask-intr)")
				}
				if d.TTYPath != "/dev/tty3" || !d.TTYReset || !d.TTYVHangup || !d.TTYVTDisallocate || d.TTYRows != 30 || d.TTYColumns != 100 {
					t.Errorf("tty fields: %q %v %v %v %d %d", d.TTYPath, d.TTYReset, d.TTYVHangup, d.TTYVTDisallocate, d.TTYRows, d.TTYColumns)
				}
			}},
		{name: "a tty on stdin defaults to the console", body: "StandardInput=tty-force\n",
			lines: map[string][]string{"tty-path": {"/dev/console"}}},
		{name: "TTYPath before StandardInput says what slinit does", body: "TTYPath=/dev/tty9\nStandardInput=tty\nStandardOutput=journal\n",
			lines: map[string][]string{"tty-path": {"/dev/tty9"}},
			warn:  []string{"NOTE", "TTYPath=/dev/tty9", "stdin, stdout and stderr"}},
		{name: "TTYPath expands specifiers", body: "TTYPath=/dev/%N\n",
			lines: map[string][]string{"tty-path": {"/dev/probe"}}},
		{name: "relative TTYPath is ignored", body: "TTYPath=tty1\n",
			lines: map[string][]string{"tty-path": nil}, warn: []string{"WARN", "TTYPath=tty1 ignored", "absolute"}},
		{name: "TTYPath with .. is ignored", body: "TTYPath=/dev/../etc/passwd\n",
			lines: map[string][]string{"tty-path": nil}, warn: []string{"WARN", "ignored", ".."}},
		{name: "not a boolean", body: "TTYReset=maybe\n",
			lines: map[string][]string{"tty-reset": nil}, warn: []string{"WARN", "TTYReset=maybe ignored"}},
		{name: "TTYReset=no is no", body: "TTYReset=off\n", lines: map[string][]string{"tty-reset": {"no"}}},
		{name: "a size beyond 16 bits", body: "TTYRows=70000\n",
			lines: map[string][]string{"tty-rows": nil}, warn: []string{"WARN", "TTYRows=70000 ignored"}},
		{name: "an empty size resets it", body: "TTYRows=30\nTTYRows=\n", lines: map[string][]string{"tty-rows": nil}},
	})
}

func TestSystemdUtmp(t *testing.T) {
	runExCases(t, []exCase{
		{name: "the last four bytes, and the line from TTYPath",
			body:  "UtmpIdentifier=pts/7\nTTYPath=/dev/pts/7\nStandardInput=tty\nUtmpMode=login\n",
			lines: map[string][]string{"inittab-id": {"ts/7"}, "inittab-line": {"pts/7"}, "utmp-mode": {"login"}},
			warn:  []string{"NOTE", "wtmp"},
			desc: func(t *testing.T, d *ServiceDescription) {
				if d.InittabID != "ts/7" || d.InittabLine != "pts/7" || d.UtmpMode != "login" {
					t.Errorf("utmp: %q %q %q", d.InittabID, d.InittabLine, d.UtmpMode)
				}
			}},
		{name: "TTYPath first", body: "TTYPath=/dev/tty1\nUtmpIdentifier=tty1\n",
			lines: map[string][]string{"inittab-id": {"tty1"}, "inittab-line": {"tty1"}}},
		{name: "no TTYPath, no line", body: "UtmpIdentifier=cons\n",
			lines: map[string][]string{"inittab-id": {"cons"}, "inittab-line": nil}},
		{name: "an empty identifier resets", body: "TTYPath=/dev/tty1\nUtmpIdentifier=tty1\nUtmpIdentifier=\n",
			lines: map[string][]string{"inittab-id": nil, "inittab-line": nil}},
		{name: "unknown mode", body: "UtmpMode=session\n",
			lines: map[string][]string{"utmp-mode": nil}, warn: []string{"WARN", "UtmpMode=session ignored"}},
	})
}

func TestParseSystemdNsec(t *testing.T) {
	for in, want := range map[string]uint64{
		"100": 100, "50us": 50000, "1.5ms": 1500000, "1s 500us": 1000500000, "2 s": 2e9, "0": 0, "3min": 180e9,
	} {
		got, inf, err := parseSystemdNsec(in)
		if err != nil || inf || got != want {
			t.Errorf("%q: %d %v %v, want %d", in, got, inf, err, want)
		}
	}
	if _, inf, err := parseSystemdNsec("infinity"); err != nil || !inf {
		t.Errorf("infinity: %v %v", inf, err)
	}
	for _, bad := range []string{"", "5x", "1.2.3", "-1", "ms"} {
		if _, _, err := parseSystemdNsec(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestSystemdTimerSlackAndSearchPath(t *testing.T) {
	runExCases(t, []exCase{
		{name: "units become nanoseconds", body: "TimerSlackNSec=50us\n",
			lines: map[string][]string{"timer-slack-nsec": {"50000"}},
			desc: func(t *testing.T, d *ServiceDescription) {
				if d.TimerSlackNsec != 50000 {
					t.Errorf("TimerSlackNsec = %d", d.TimerSlackNsec)
				}
			}},
		{name: "infinity leaves it alone", body: "TimerSlackNSec=10\nTimerSlackNSec=infinity\n",
			lines: map[string][]string{"timer-slack-nsec": {"0"}}},
		{name: "not a span", body: "TimerSlackNSec=fast\n",
			lines: map[string][]string{"timer-slack-nsec": nil}, warn: []string{"WARN", "TimerSlackNSec=fast ignored"}},
		{name: "search path accumulates", body: "ExecSearchPath=/opt/a:/opt/b/\nExecSearchPath=/usr/local/bin\n",
			lines: map[string][]string{"exec-search-path": {"/opt/a:/opt/b:/usr/local/bin"}},
			desc: func(t *testing.T, d *ServiceDescription) {
				if d.ExecSearchPath != "/opt/a:/opt/b:/usr/local/bin" {
					t.Errorf("ExecSearchPath = %q", d.ExecSearchPath)
				}
			}},
		{name: "a $ stays a $", body: "ExecSearchPath=/opt/$x\n",
			desc: func(t *testing.T, d *ServiceDescription) {
				if d.ExecSearchPath != "/opt/$x" {
					t.Errorf("ExecSearchPath = %q", d.ExecSearchPath)
				}
			}},
		{name: "an empty assignment resets", body: "ExecSearchPath=/opt/a\nExecSearchPath=\nExecSearchPath=/opt/c\n",
			lines: map[string][]string{"exec-search-path": {"/opt/c"}}},
		{name: "a relative path ends the line", body: "ExecSearchPath=/a:bin:/b\n",
			lines: map[string][]string{"exec-search-path": {"/a"}}, warn: []string{"WARN", "not an absolute path"}},
	})
}

func TestSystemdLogFilters(t *testing.T) {
	runExCases(t, []exCase{
		{name: "level name", body: "LogLevelMax=warning\n",
			lines: map[string][]string{"log-level-max": {"warning"}},
			desc: func(t *testing.T, d *ServiceDescription) {
				if d.LogLevelMax != 4 {
					t.Errorf("LogLevelMax = %d", d.LogLevelMax)
				}
			}},
		{name: "level number", body: "LogLevelMax=5\n", lines: map[string][]string{"log-level-max": {"5"}}},
		{name: "systemd's names are lower case", body: "LogLevelMax=Warning\n",
			lines: map[string][]string{"log-level-max": nil}, warn: []string{"WARN", "LogLevelMax=Warning ignored"}},
		{name: "interval alone gets journald's burst", body: "LogRateLimitIntervalSec=1min\n",
			lines: map[string][]string{"log-rate-limit-interval": {"60s"}, "log-rate-limit-burst": {"10000"}},
			desc: func(t *testing.T, d *ServiceDescription) {
				if d.LogRateLimitInterval.Seconds() != 60 || d.LogRateLimitBurst != 10000 {
					t.Errorf("rate limit %v/%d", d.LogRateLimitInterval, d.LogRateLimitBurst)
				}
			}},
		{name: "burst alone gets journald's interval", body: "LogRateLimitBurst=50\n",
			lines: map[string][]string{"log-rate-limit-interval": {"30s"}, "log-rate-limit-burst": {"50"}}},
		{name: "both", body: "LogRateLimitBurst=50\nLogRateLimitIntervalSec=500ms\n",
			lines: map[string][]string{"log-rate-limit-interval": {"0.5s"}, "log-rate-limit-burst": {"50"}}},
		{name: "infinite interval", body: "LogRateLimitIntervalSec=infinity\n",
			lines: map[string][]string{"log-rate-limit-interval": nil}, warn: []string{"WARN", "ignored"}},
		{name: "patterns, caseless without a capital", body: "LogFilterPatterns=foo\nLogFilterPatterns=~Bar\n",
			lines: map[string][]string{"log-include": {"(?i)foo"}, "log-exclude": {"Bar"}},
			desc: func(t *testing.T, d *ServiceDescription) {
				if !reflect.DeepEqual(d.LogInclude, []string{"(?i)foo"}) || !reflect.DeepEqual(d.LogExclude, []string{"Bar"}) {
					t.Errorf("include %q exclude %q", d.LogInclude, d.LogExclude)
				}
			}},
		{name: "an empty assignment resets both", body: "LogFilterPatterns=foo\nLogFilterPatterns=~bar\nLogFilterPatterns=\n",
			lines: map[string][]string{"log-include": nil, "log-exclude": nil}},
		{name: "a pattern RE2 cannot take", body: "LogFilterPatterns=(?<=x)y\n",
			lines: map[string][]string{"log-include": nil}, warn: []string{"WARN", "PCRE2"}},
		{name: "a bare ~", body: "LogFilterPatterns=~\n", warn: []string{"WARN", "LogFilterPatterns=~ ignored"}},
	})
}

func TestSystemdStdio(t *testing.T) {
	runExCases(t, []exCase{
		{name: "append", body: "StandardOutput=append:/var/log/probe.log\n",
			lines: map[string][]string{"logfile": {"/var/log/probe.log"}},
			desc: func(t *testing.T, d *ServiceDescription) {
				if d.LogFile != "/var/log/probe.log" || d.LogType != service.LogToFile {
					t.Errorf("logfile %q type %v", d.LogFile, d.LogType)
				}
			}},
		{name: "file: and truncate: append, with a note", body: "StandardOutput=truncate:/var/log/probe.log\n",
			lines: map[string][]string{"logfile": {"/var/log/probe.log"}}, warn: []string{"NOTE", "truncates", "appends"}},
		{name: "two files", body: "StandardOutput=file:/var/log/p.out\nStandardError=append:/var/log/p.err\n",
			lines: map[string][]string{"logfile": {"/var/log/p.out"}, "stderr-logfile": {"/var/log/p.err"}},
			desc: func(t *testing.T, d *ServiceDescription) {
				if d.StderrLogFile != "/var/log/p.err" {
					t.Errorf("StderrLogFile = %q", d.StderrLogFile)
				}
			}},
		{name: "one file for both is the logfile", body: "StandardError=append:/var/log/p\nStandardOutput=append:/var/log/p\n",
			lines: map[string][]string{"logfile": {"/var/log/p"}, "stderr-logfile": nil}},
		{name: "null", body: "StandardOutput=null\nStandardError=null\nStandardInput=null\n",
			lines: map[string][]string{"close-stdout": {"yes"}, "close-stderr": {"yes"}, "close-stdin": {"yes"}},
			desc: func(t *testing.T, d *ServiceDescription) {
				if !d.CloseStdout || !d.CloseStderr || !d.CloseStdin {
					t.Errorf("close: %v %v %v", d.CloseStdin, d.CloseStdout, d.CloseStderr)
				}
			}},
		{name: "stderr to null cannot leave a logfile", body: "StandardOutput=append:/x\nStandardError=null\n",
			lines: map[string][]string{"close-stderr": nil}, warn: []string{"WARN", "StandardError=null", "not carried"}},
		{name: "a value systemd refuses keeps the earlier one", body: "StandardOutput=append:/x\nStandardOutput=bogus\n",
			lines: map[string][]string{"logfile": {"/x"}}, warn: []string{"WARN", "StandardOutput=bogus ignored"}},
		{name: "journal after a file resets it", body: "StandardOutput=append:/x\nStandardOutput=journal\n",
			lines: map[string][]string{"logfile": nil}},
		{name: "relative file", body: "StandardOutput=append:log\n",
			lines: map[string][]string{"logfile": nil}, warn: []string{"WARN", "not an absolute path"}},
		{name: "kmsg", body: "StandardOutput=kmsg\n", warn: []string{"NOTE", "StandardOutput=kmsg", "no slinit equivalent"}},
		{name: "stderr file for a oneshot", body: "Type=oneshot\nStandardError=append:/e\n",
			warn: []string{"WARN", "type = process only"}},
		{name: "stdin from a file", body: "StandardInput=file:/etc/hostname\n",
			lines: map[string][]string{"standard-input-file": {"/etc/hostname"}},
			desc: func(t *testing.T, d *ServiceDescription) {
				if d.StandardInputFile != "/etc/hostname" {
					t.Errorf("StandardInputFile = %q", d.StandardInputFile)
				}
			}},
		{name: "a file on stdin wins over data", body: "StandardInputText=x\nStandardInput=file:/etc/hostname\nStandardInputText=y\n",
			lines: map[string][]string{"standard-input-file": {"/etc/hostname"}, "standard-input-data": nil}},
		{name: "socket stdin", body: "StandardInput=socket\n", warn: []string{"NOTE", "no slinit equivalent"}},
		{name: "bogus stdin", body: "StandardInput=keyboard\n", warn: []string{"WARN", "StandardInput=keyboard ignored"}},
	})
}

func TestSystemdStandardInputData(t *testing.T) {
	_, _, d := exConvert(t, "StandardInputText=hello\\tworld\nStandardInputText=cost $5 for %n\nStandardInputData=AAE=\n")
	want := "hello\tworld\ncost $5 for probe.service\n\x00\x01"
	if string(d.StandardInput) != want || !d.StandardInputSet {
		t.Errorf("stdin %q, want %q", d.StandardInput, want)
	}
	if _, _, d := exConvert(t, "StandardInputText=a\nStandardInputText=\nStandardInputText=b\n"); string(d.StandardInput) != "b\n" {
		t.Errorf("after a reset: %q", d.StandardInput)
	}
	if _, w, d := exConvert(t, "StandardInputText=bad \\q escape\n"); d.StandardInputSet || !exHas(w, "WARN", "ignored") {
		t.Errorf("bad escape: set=%v warnings %v", d.StandardInputSet, w)
	}
	if _, w, _ := exConvert(t, "StandardInputData=!!!\n"); !exHas(w, "WARN", "base64") {
		t.Errorf("bad base64: %v", w)
	}
}

func TestSystemdOpenFile(t *testing.T) {
	runExCases(t, []exCase{
		{name: "path, name, option", body: "OpenFile=/etc/hostname:host:read-only\n",
			lines: map[string][]string{"open-file": {"/etc/hostname:host:read-only"}},
			desc: func(t *testing.T, d *ServiceDescription) {
				want := []OpenFileSpec{{Path: "/etc/hostname", FDName: "host", Options: "read-only"}}
				if !reflect.DeepEqual(d.OpenFiles, want) {
					t.Errorf("OpenFiles = %+v", d.OpenFiles)
				}
			}},
		{name: "the name defaults to the file's", body: "OpenFile=/etc/hostname::graceful\n",
			lines: map[string][]string{"open-file": {"/etc/hostname:hostname:graceful"}}, warn: []string{"NOTE", "/dev/null"}},
		{name: "a trailing colon", body: "OpenFile=/etc/hostname:h:append:\n",
			lines: map[string][]string{"open-file": {"/etc/hostname:h:append"}}},
		{name: "exclusive modes", body: "OpenFile=/x:a:read-only,append\n",
			lines: map[string][]string{"open-file": nil}, warn: []string{"WARN", "exclude each other"}},
		{name: "unknown option", body: "OpenFile=/x:a:sticky\n", warn: []string{"WARN", "unknown option"}},
		{name: "relative", body: "OpenFile=x\n", warn: []string{"WARN", "absolute"}},
		{name: "four fields", body: "OpenFile=/x:a:append:b\n", warn: []string{"WARN", "three"}},
		{name: "a colon in the path", body: "OpenFile=/a\\x3ab\n", warn: []string{"WARN", "colon"}},
		{name: "one survives", body: "OpenFile=/etc/a\nOpenFile=/etc/b\n",
			lines: map[string][]string{"open-file": {"/etc/b:b"}}, warn: []string{"WARN", "keeping the last"}},
	})
}

func TestSystemdCredentials(t *testing.T) {
	dir := t.TempDir()
	runExCases(t, []exCase{
		{name: "load a file", body: "LoadCredential=key:/etc/hostname\n",
			lines: map[string][]string{"load-credential": {"key:/etc/hostname"}},
			desc: func(t *testing.T, d *ServiceDescription) {
				want := []process.CredentialSource{{Name: "key", Path: "/etc/hostname"}}
				if !reflect.DeepEqual(d.Credentials, want) {
					t.Errorf("Credentials = %+v", d.Credentials)
				}
			}},
		{name: "a name from the stores", body: "LoadCredential=key\n",
			lines: map[string][]string{"load-credential": nil}, warn: []string{"WARN", "import-credential = key"}},
		{name: "a directory", body: "LoadCredential=key:" + dir + "\n",
			lines: map[string][]string{"load-credential": nil}, warn: []string{"WARN", "no regular file"}},
		{name: "a bad name", body: "LoadCredential=a/b:/etc/hostname\n", warn: []string{"WARN", "not a valid credential name"}},
		{name: "one per name", body: "LoadCredential=a:/etc/hostname\nLoadCredential=a:/etc/passwd\n",
			lines: map[string][]string{"load-credential": {"a:/etc/passwd"}}},
		{name: "reset", body: "LoadCredential=a:/etc/hostname\nLoadCredential=\n",
			lines: map[string][]string{"load-credential": nil}},
		{name: "escapes decoded, $ kept", body: "SetCredential=greet:hello\\x20world, $5\n",
			lines: map[string][]string{"set-credential": {"greet:hello world, $$5"}},
			desc: func(t *testing.T, d *ServiceDescription) {
				if len(d.Credentials) != 1 || d.Credentials[0].Value != "hello world, $5" {
					t.Errorf("Credentials = %+v", d.Credentials)
				}
			}},
		{name: "a newline cannot be written", body: "SetCredential=nl:line\\n\n",
			lines: map[string][]string{"set-credential": nil}, warn: []string{"WARN", "newline"}},
		{name: "a NUL cannot be written", body: "SetCredential=z:a\\x00b\n", warn: []string{"WARN", "NUL"}},
		{name: "no value", body: "SetCredential=x:\n", warn: []string{"WARN", "no value"}},
		{name: "set is the fallback of load, whichever comes first",
			body:  "LoadCredential=k:/etc/hostname\nSetCredential=k:fallback\n",
			lines: map[string][]string{"set-credential": {"k:fallback"}, "load-credential": {"k:/etc/hostname"}},
			warn:  []string{"NOTE", "falls back to the value"},
			desc: func(t *testing.T, d *ServiceDescription) {
				// slinit writes them in order: the file goes last and wins.
				if len(d.Credentials) != 2 || d.Credentials[1].Path != "/etc/hostname" {
					t.Errorf("Credentials = %+v", d.Credentials)
				}
			}},
		{name: "import a glob", body: "ImportCredential=probe.*\n",
			lines: map[string][]string{"import-credential": {"probe.*"}},
			desc: func(t *testing.T, d *ServiceDescription) {
				if !reflect.DeepEqual(d.ImportCredentials, []string{"probe.*"}) {
					t.Errorf("ImportCredentials = %q", d.ImportCredentials)
				}
			}},
		{name: "a glob systemd refuses", body: "ImportCredential=a*b\n", warn: []string{"WARN", "not a valid credential"}},
		{name: "a rename", body: "ImportCredential=a.*:b\n",
			lines: map[string][]string{"import-credential": nil}, warn: []string{"WARN", "rename"}},
		{name: "every pattern is kept", body: "ImportCredential=a.*\nImportCredential=b.*\nImportCredential=a.*\n",
			lines: map[string][]string{"import-credential": {"a.*"}},
			desc: func(t *testing.T, d *ServiceDescription) {
				if !reflect.DeepEqual(d.ImportCredentials, []string{"a.*", "b.*"}) {
					t.Errorf("ImportCredentials = %q", d.ImportCredentials)
				}
			}},
		{name: "an empty pattern resets the list", body: "ImportCredential=a.*\nImportCredential=\nImportCredential=b.*\n",
			desc: func(t *testing.T, d *ServiceDescription) {
				if !reflect.DeepEqual(d.ImportCredentials, []string{"b.*"}) {
					t.Errorf("ImportCredentials = %q", d.ImportCredentials)
				}
			}},
	})

	// SetCredential= first: still written before the load-credential.
	out, _, _ := exConvert(t, "SetCredential=k:fallback\nLoadCredential=k:/etc/hostname\n")
	if s, l := strings.Index(out, "set-credential = k:"), strings.Index(out, "load-credential = k:"); s < 0 || l < 0 || s > l {
		t.Errorf("set-credential must precede load-credential:\n%s", out)
	}
}

func TestSystemdEnvironmentFilters(t *testing.T) {
	runExCases(t, []exCase{
		{name: "names add up", body: "UnsetEnvironment=FOO BAR\nUnsetEnvironment=\"BAZ\" QUX=1 1bad\n",
			lines: map[string][]string{"unset-environment": {"FOO BAR BAZ"}},
			warn:  []string{"WARN", "QUX=1", "names only"},
			desc: func(t *testing.T, d *ServiceDescription) {
				if !reflect.DeepEqual(d.UnsetEnvironment, []string{"FOO", "BAR", "BAZ"}) {
					t.Errorf("UnsetEnvironment = %q", d.UnsetEnvironment)
				}
			}},
		{name: "reset", body: "UnsetEnvironment=FOO\nUnsetEnvironment=\n",
			lines: map[string][]string{"unset-environment": nil}},
	})
	if _, ok := sdServiceRules["PassEnvironment"]; ok {
		t.Error("PassEnvironment is mapped, though slinit's pass-environment also drops the env-file variables")
	}
	if g, ok := sdServiceNoEquivalent["PassEnvironment"]; !ok || g.why == "" {
		t.Error("PassEnvironment has no gap entry")
	}
}

func TestSystemdExecStopPost(t *testing.T) {
	runExCases(t, []exCase{
		{name: "plain", body: "ExecStopPost=/bin/rm -f /run/probe.pid\n",
			warn: []string{"NOTE", "finish-command"},
			desc: func(t *testing.T, d *ServiceDescription) {
				want := append([]string{"/bin/sh", "-c", `n=$#; while [ "$n" -gt 2 ]; do set -- "$@" "$1"; shift; n=$((n-1)); done; shift 2; exec "$@"`, "sh"},
					"/bin/rm", "-f", "/run/probe.pid")
				if !reflect.DeepEqual(d.FinishCommand, want) || d.Privileged.Finish {
					t.Errorf("FinishCommand = %q priv %v", d.FinishCommand, d.Privileged.Finish)
				}
			}},
		{name: "+ is slinit's +", body: "ExecStopPost=+/bin/true\n",
			desc: func(t *testing.T, d *ServiceDescription) {
				if !d.Privileged.Finish {
					t.Error("not privileged")
				}
			}},
		{name: "! runs it as root too", body: "ExecStopPost=!/bin/true\n",
			desc: func(t *testing.T, d *ServiceDescription) {
				if !d.Privileged.Finish {
					t.Error("not privileged")
				}
			}},
		{name: "one finish-command", body: "ExecStopPost=/bin/true\nExecStopPost=/bin/false\n",
			warn: []string{"WARN", "keeping the first"},
			desc: func(t *testing.T, d *ServiceDescription) {
				if last := d.FinishCommand[len(d.FinishCommand)-1]; last != "/bin/true" {
					t.Errorf("kept %q", d.FinishCommand)
				}
			}},
		{name: "reset", body: "ExecStopPost=/bin/true\nExecStopPost=\n",
			lines: map[string][]string{"finish-command": nil}},
		{name: "a oneshot runs none", body: "Type=oneshot\nExecStopPost=/bin/true\n",
			warn: []string{"WARN", "type = process only"}},
	})
}

// The wrapper drops exactly the exit code and signal number slinit
// appends, and passes every other argument through untouched.
func TestSystemdExecStopPostDropsSlinitArgs(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	_, _, d := exConvert(t, `ExecStopPost=/bin/sh -c 'printf "%%s|" "$@"' x "a  b" "" 'a$b'`+"\n")
	argv := append(append([]string(nil), d.FinishCommand...), "0", "15")
	out, err := exec.Command(argv[0], argv[1:]...).Output()
	if err != nil {
		t.Fatalf("%q: %v", argv, err)
	}
	if want := "a  b||a$b|"; string(out) != want {
		t.Errorf("finish-command printed %q, want %q (argv %q)", out, want, argv)
	}
}

func TestSystemdExecReload(t *testing.T) {
	runExCases(t, []exCase{
		{name: "kill -HUP $MAINPID", body: "ExecReload=/bin/kill -HUP $MAINPID\n",
			lines: map[string][]string{"reload-signal": {"HUP"}},
			desc: func(t *testing.T, d *ServiceDescription) {
				if d.ReloadSignal != syscall.SIGHUP {
					t.Errorf("ReloadSignal = %v", d.ReloadSignal)
				}
			}},
		{name: "kill -s USR1 ${MAINPID}", body: "ExecReload=/usr/bin/kill -s USR1 ${MAINPID}\n",
			lines: map[string][]string{"reload-signal": {"USR1"}}},
		{name: "a reload command", body: "ExecReload=/usr/sbin/nginx -s reload\n",
			lines: map[string][]string{"reload-signal": nil, "reload-command": {"/usr/sbin/nginx -s reload"}},
			desc: func(t *testing.T, d *ServiceDescription) {
				if strings.Join(d.ReloadCommand, " ") != "/usr/sbin/nginx -s reload" {
					t.Errorf("ReloadCommand = %q", d.ReloadCommand)
				}
			}},
		{name: "a second command is dropped", body: "ExecReload=/bin/a\nExecReload=/bin/b\n",
			lines: map[string][]string{"reload-command": {"/bin/a"}}, warn: []string{"WARN", "keeping the first"}},
		{name: "+ is root", body: "ExecReload=+/bin/a\n",
			lines: map[string][]string{"reload-command": {"+/bin/a"}},
			desc: func(t *testing.T, d *ServiceDescription) {
				if !d.Privileged.Reload {
					t.Error("+ not carried")
				}
			}},
		{name: "a command and a signal", body: "ExecReload=/bin/a\nExecReload=/bin/kill -HUP $MAINPID\n",
			lines: map[string][]string{"reload-command": {"/bin/a"}, "reload-signal": {"HUP"}}},
		{name: "reset drops the command", body: "ExecReload=/bin/a\nExecReload=\n",
			lines: map[string][]string{"reload-command": nil}},
		{name: "reset", body: "ExecReload=/bin/kill -HUP $MAINPID\nExecReload=\n",
			lines: map[string][]string{"reload-signal": nil}},
	})
}

func TestSystemdExecGaps(t *testing.T) {
	for key, val := range map[string]string{
		"LoadCredentialEncrypted": "k:/etc/k.cred", "SetCredentialEncrypted": "k:AAAA",
		"LogNamespace": "ns", "SyslogFacility": "daemon",
	} {
		out, w, _ := exConvert(t, key+"="+val+"\n")
		if !exHas(w, "NOTE", key+"="+val, "no slinit equivalent") {
			t.Errorf("%s: %v", key, w)
		}
		if strings.Contains(out, val) {
			t.Errorf("%s=%s emitted something:\n%s", key, val, out)
		}
	}
}

// Nothing here may leave a file Parse rejects: every emitted value went
// through slinit's own parser, but a combination might not.
func TestSystemdExecMappingsLoadTogether(t *testing.T) {
	_, _, d := exConvert(t, strings.Join([]string{
		"StandardInput=tty", "TTYPath=/dev/tty2", "TTYReset=yes", "UtmpIdentifier=tty2", "UtmpMode=user",
		"StandardOutput=append:/var/log/p", "StandardError=append:/var/log/p.err", "LogLevelMax=info",
		"LogRateLimitBurst=5", "LogFilterPatterns=x", "TimerSlackNSec=1ms", "ExecSearchPath=/opt/bin",
		"LoadCredential=a:/etc/hostname", "SetCredential=b:c", "ImportCredential=x.*", "UnsetEnvironment=Q",
		"OpenFile=/etc/hostname", "ExecStopPost=/bin/true", "ExecReload=/bin/kill -USR2 $MAINPID", "",
	}, "\n"))
	if d.TTYPath != "/dev/tty2" || d.LogFile != "/var/log/p" || d.ReloadSignal != syscall.SIGUSR2 || len(d.FinishCommand) == 0 {
		t.Errorf("description: tty %q log %q reload %v finish %q", d.TTYPath, d.LogFile, d.ReloadSignal, d.FinishCommand)
	}
}

// sdOrderResult is one conversion of a unit made of groups of lines.
type sdOrderResult struct {
	how   string
	out   string
	warns []SystemdWarning
	desc  *ServiceDescription
}

// sdOrders converts a unit whose [Service] lines come in two groups, in
// every arrangement systemd reads alike: a then b, b then a, a in the
// unit with b in a drop-in, and b in the unit with a in a drop-in. The
// lines inside a group keep their order (a reset only resets what
// comes before it). check runs on each.
func sdOrders(t *testing.T, a, b string, check func(t *testing.T, r sdOrderResult)) {
	t.Helper()
	const head = "[Service]\nExecStart=/bin/sleep 1\n"
	for _, v := range []struct{ how, unit, dropin string }{
		{"a,b", head + a + "\n" + b + "\n", ""},
		{"b,a", head + b + "\n" + a + "\n", ""},
		{"a; drop-in b", head + a + "\n", "[Service]\n" + b + "\n"},
		{"b; drop-in a", head + b + "\n", "[Service]\n" + a + "\n"},
	} {
		t.Run(v.how, func(t *testing.T) {
			dir := t.TempDir()
			p := writeUnit(t, dir, "ord.service", v.unit)
			if v.dropin != "" {
				writeUnit(t, filepath.Join(dir, "ord.service.d"), "50-x.conf", v.dropin)
			}
			cfg, warns, err := ConvertSystemdUnitIn(p, []string{dir})
			if err != nil {
				t.Fatalf("convert: %v", err)
			}
			var buf bytes.Buffer
			EmitSlinitFile(&buf, cfg)
			desc, err := SystemdUnitToServiceDescription(p, "ord", dir)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			check(t, sdOrderResult{v.how, buf.String(), warns, desc})
		})
	}
}

// sdWant checks the emitted values of key; nil wants none.
func sdWant(t *testing.T, r sdOrderResult, key string, want ...string) {
	t.Helper()
	if got := exLines(r.out, key); !reflect.DeepEqual(got, want) && !(len(got) == 0 && len(want) == 0) {
		t.Errorf("%s: %s = %q, want %q\n%s", r.how, key, got, want, r.out)
	}
}

func sdWarn(t *testing.T, r sdOrderResult, level string, subs ...string) {
	t.Helper()
	if !exHas(r.warns, level, subs...) {
		t.Errorf("%s: no %s holding %q in %v", r.how, level, subs, r.warns)
	}
}

// ExecReload= and ReloadSignal= name one signal between them, whatever
// their order: the review found an empty ExecReload= dropping the
// ReloadSignal=, and the later line deciding the signal.
func TestSystemdReloadSignalOrder(t *testing.T) {
	t.Run("empty ExecReload= keeps ReloadSignal=", func(t *testing.T) {
		sdOrders(t, "ExecReload=/bin/kill -HUP $MAINPID\nExecReload=", "ReloadSignal=SIGUSR2", func(t *testing.T, r sdOrderResult) {
			sdWant(t, r, "reload-signal", "SIGUSR2")
		})
	})
	t.Run("ExecReload= decides without notify-reload", func(t *testing.T) {
		sdOrders(t, "ExecReload=/bin/kill -HUP $MAINPID", "ReloadSignal=SIGUSR2", func(t *testing.T, r sdOrderResult) {
			sdWant(t, r, "reload-signal", "HUP")
			sdWarn(t, r, "NOTE", "ReloadSignal=SIGUSR2", "notify-reload only")
			if r.desc.ReloadSignal != syscall.SIGHUP {
				t.Errorf("%s: ReloadSignal = %v", r.how, r.desc.ReloadSignal)
			}
		})
	})
	t.Run("ReloadSignal= decides with notify-reload", func(t *testing.T) {
		sdOrders(t, "ExecReload=/bin/kill -HUP $MAINPID\nReloadSignal=SIGUSR2", "Type=notify-reload", func(t *testing.T, r sdOrderResult) {
			sdWant(t, r, "reload-signal", "SIGUSR2")
			sdWarn(t, r, "NOTE", "ExecReload=", "SIGHUP", "notify-reload")
		})
	})
	t.Run("notify-reload defaults to SIGHUP", func(t *testing.T) {
		sdOrders(t, "Type=notify-reload", "ReloadSignal=USR1\nReloadSignal=", func(t *testing.T, r sdOrderResult) {
			sdWant(t, r, "reload-signal", "SIGHUP")
		})
	})
	t.Run("a dropped reload command is reset too", func(t *testing.T) {
		sdOrders(t, "ExecReload=/usr/sbin/nginx -s reload\nExecReload=", "ReloadSignal=USR1", func(t *testing.T, r sdOrderResult) {
			sdWant(t, r, "reload-signal", "SIGUSR1")
			if exHas(r.warns, "WARN", "no reload command") {
				t.Errorf("%s: warning for a reset ExecReload=: %v", r.how, r.warns)
			}
		})
	})
}

// A SetCredential= backs a LoadCredential= of its name: systemd uses it
// when the file is missing, where slinit would fail the start.
func TestSystemdCredentialFallback(t *testing.T) {
	present := filepath.Join(t.TempDir(), "present")
	writeFile(t, present, "secret")
	missing := filepath.Join(t.TempDir(), "missing")
	t.Run("missing file", func(t *testing.T) {
		sdOrders(t, "LoadCredential=k:"+missing, "SetCredential=k:fallback", func(t *testing.T, r sdOrderResult) {
			sdWant(t, r, "load-credential")
			sdWant(t, r, "set-credential", "k:fallback")
			sdWarn(t, r, "NOTE", "dropped", "missing")
			if len(r.desc.Credentials) != 1 || r.desc.Credentials[0].Value != "fallback" {
				t.Errorf("%s: Credentials = %+v", r.how, r.desc.Credentials)
			}
		})
	})
	t.Run("present file", func(t *testing.T) {
		sdOrders(t, "LoadCredential=k:"+present, "SetCredential=k:fallback", func(t *testing.T, r sdOrderResult) {
			sdWant(t, r, "load-credential", "k:"+present)
			sdWant(t, r, "set-credential", "k:fallback")
			if s, l := strings.Index(r.out, "set-credential = "), strings.Index(r.out, "load-credential = "); s > l {
				t.Errorf("%s: set-credential must precede load-credential:\n%s", r.how, r.out)
			}
		})
	})
	t.Run("a missing file without a fallback stays", func(t *testing.T) {
		sdOrders(t, "LoadCredential=k:"+missing, "SetCredential=other:v", func(t *testing.T, r sdOrderResult) {
			sdWant(t, r, "load-credential", "k:"+missing)
		})
	})
	t.Run("each empty assignment resets its own list", func(t *testing.T) {
		sdOrders(t, "LoadCredential=a:"+present+"\nLoadCredential=", "SetCredential=b:v", func(t *testing.T, r sdOrderResult) {
			sdWant(t, r, "load-credential")
			sdWant(t, r, "set-credential", "b:v")
		})
		sdOrders(t, "SetCredential=b:v\nSetCredential=", "LoadCredential=a:"+present, func(t *testing.T, r sdOrderResult) {
			sdWant(t, r, "set-credential")
			sdWant(t, r, "load-credential", "a:"+present)
		})
	})
	t.Run("import patterns add up", func(t *testing.T) {
		sdOrders(t, "ImportCredential=a.*", "ImportCredential=b.*", func(t *testing.T, r sdOrderResult) {
			if len(r.desc.ImportCredentials) != 2 || !strings.Contains(r.out, "import-credential += ") {
				t.Errorf("%s: ImportCredentials = %q\n%s", r.how, r.desc.ImportCredentials, r.out)
			}
		})
	})
}

// The three streams and the TTY are settled from the last value of
// each, whatever order the lines and drop-ins give them.
func TestSystemdStdioOrder(t *testing.T) {
	t.Run("a later null on stdin drops the tty", func(t *testing.T) {
		sdOrders(t, "StandardInput=tty\nStandardInput=null", "TTYReset=no", func(t *testing.T, r sdOrderResult) {
			sdWant(t, r, "tty-path")
			sdWant(t, r, "close-stdin", "yes")
			sdWant(t, r, "log-type", "journal")
		})
	})
	t.Run("a later journal on stdout drops the tty", func(t *testing.T) {
		sdOrders(t, "StandardOutput=tty\nStandardOutput=journal", "StandardError=inherit", func(t *testing.T, r sdOrderResult) {
			sdWant(t, r, "tty-path")
			sdWant(t, r, "log-type", "journal")
		})
	})
	t.Run("data after a file", func(t *testing.T) {
		sdOrders(t, "StandardInput=file:/etc/hostname\nStandardInput=data", "StandardInputText=hi", func(t *testing.T, r sdOrderResult) {
			sdWant(t, r, "standard-input-file")
			if string(r.desc.StandardInput) != "hi\n" {
				t.Errorf("%s: stdin %q", r.how, r.desc.StandardInput)
			}
		})
	})
	t.Run("data overrides the default and an explicit null", func(t *testing.T) {
		sdOrders(t, "StandardInput=null", "StandardInputText=hi", func(t *testing.T, r sdOrderResult) {
			sdWant(t, r, "close-stdin")
			if string(r.desc.StandardInput) != "hi\n" {
				t.Errorf("%s: stdin %q", r.how, r.desc.StandardInput)
			}
		})
	})
	t.Run("a file wins over data", func(t *testing.T) {
		sdOrders(t, "StandardInput=file:/etc/hostname", "StandardInputText=hi", func(t *testing.T, r sdOrderResult) {
			sdWant(t, r, "standard-input-file", "/etc/hostname")
			sdWant(t, r, "standard-input-data")
			sdWarn(t, r, "NOTE", "unused")
		})
	})
	t.Run("TTYPath and a tty stdin", func(t *testing.T) {
		sdOrders(t, "TTYPath=/dev/tty3", "StandardInput=tty\nUtmpIdentifier=tty3", func(t *testing.T, r sdOrderResult) {
			sdWant(t, r, "tty-path", "/dev/tty3")
			sdWant(t, r, "inittab-line", "tty3")
			if exHas(r.warns, "NOTE", "stdin, stdout and stderr") {
				t.Errorf("%s: all three streams are the tty, yet a note: %v", r.how, r.warns)
			}
		})
	})
	t.Run("a tty on stdout only", func(t *testing.T) {
		sdOrders(t, "TTYPath=/dev/tty3", "StandardOutput=tty", func(t *testing.T, r sdOrderResult) {
			sdWant(t, r, "tty-path", "/dev/tty3")
			sdWarn(t, r, "NOTE", "stdin, stdout and stderr", "stdin null")
		})
	})
	t.Run("stderr to null with a logfile", func(t *testing.T) {
		sdOrders(t, "StandardOutput=append:/var/log/x", "StandardError=null", func(t *testing.T, r sdOrderResult) {
			sdWant(t, r, "logfile", "/var/log/x")
			sdWant(t, r, "close-stderr")
			sdWarn(t, r, "WARN", "StandardError=null", "not carried")
		})
	})
	t.Run("stderr to its own file", func(t *testing.T) {
		sdOrders(t, "StandardOutput=append:/var/log/x", "StandardError=append:/var/log/e", func(t *testing.T, r sdOrderResult) {
			sdWant(t, r, "logfile", "/var/log/x")
			sdWant(t, r, "stderr-logfile", "/var/log/e")
		})
	})
}

// What only a type = process service runs is dropped, with a word, from
// a oneshot, whether Type= comes before or after it.
func TestSystemdProcessOnlyOrder(t *testing.T) {
	sdOrders(t, "ExecStopPost=/bin/true\nStandardError=append:/var/log/e", "Type=oneshot", func(t *testing.T, r sdOrderResult) {
		sdWant(t, r, "finish-command")
		sdWant(t, r, "stderr-logfile")
		sdWarn(t, r, "WARN", "ExecStopPost=", "type = process only")
		sdWarn(t, r, "WARN", "StandardError=append:/var/log/e", "type = process only")
	})
}
