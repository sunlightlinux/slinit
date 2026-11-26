package config

import (
	"path/filepath"
	"strings"
	"testing"

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
