package config

import (
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
	}
	for _, c := range cases {
		direct, _ := loadSystemdDirect(t, c.unit)
		for name, d := range map[string]*ServiceDescription{"converted": parseEmitted(t, c.unit), "direct": direct} {
			if d.StartTimeout != c.start || d.StopTimeout != c.stop {
				t.Errorf("%s %s: start-timeout = %v, stop-timeout = %v; want %v, %v",
					c.name, name, d.StartTimeout, d.StopTimeout, c.start, c.stop)
			}
		}
	}
}
