package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// The names follow unit_file_expand_dropin_names in systemd's
// src/shared/dropin.c, traced by hand for each case.
func TestSystemdDropinNames(t *testing.T) {
	tests := []struct {
		unit string
		want []string
	}{
		{"foo.service", []string{"foo.service", "service"}},
		{"foo-bar-baz.service", []string{"foo-bar-baz.service", "foo-bar-.service", "foo-.service", "service"}},
		{"foo-bar@x.service", []string{
			"foo-bar@x.service", "foo-bar@.service", "foo-.service",
			"foo-@x.service", "foo-@.service", "service"}},
		// A leading dash ends the walk; so does a name with no dash.
		{"-foo.service", []string{"-foo.service", "service"}},
	}
	for _, tc := range tests {
		if got := systemdDropinNames(tc.unit); !slices.Equal(got, tc.want) {
			t.Errorf("systemdDropinNames(%q) =\n  %q\nwant\n  %q", tc.unit, got, tc.want)
		}
	}
}

func writeTree(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, body)
}

func TestSystemdDropinPrecedence(t *testing.T) {
	root := t.TempDir()
	etc, lib := filepath.Join(root, "etc"), filepath.Join(root, "lib")
	writeTree(t, filepath.Join(lib, "foo-bar.service.d", "10-x.conf"), "lib 10")
	writeTree(t, filepath.Join(etc, "foo-bar.service.d", "10-x.conf"), "etc 10") // overrides lib's
	writeTree(t, filepath.Join(lib, "foo-bar.service.d", "20-y.conf"), "lib 20")
	writeTree(t, filepath.Join(lib, "foo-.service.d", "15-prefix.conf"), "prefix 15")
	writeTree(t, filepath.Join(lib, "service.d", "05-all.conf"), "all 05")
	writeTree(t, filepath.Join(lib, "service.d", "20-y.conf"), "loses to foo-bar's 20-y")
	writeTree(t, filepath.Join(lib, "foo-bar.service.d", "notes.txt"), "not a .conf")
	// A /dev/null symlink in a higher-priority directory masks the file.
	writeTree(t, filepath.Join(lib, "foo-bar.service.d", "30-masked.conf"), "masked")
	if err := os.Symlink("/dev/null", filepath.Join(etc, "foo-bar.service.d", "30-masked.conf")); err != nil {
		t.Fatal(err)
	}

	got := systemdDropins([]string{etc, lib}, "foo-bar.service")
	want := []string{
		filepath.Join(lib, "service.d", "05-all.conf"),
		filepath.Join(etc, "foo-bar.service.d", "10-x.conf"),
		filepath.Join(lib, "foo-.service.d", "15-prefix.conf"),
		filepath.Join(lib, "foo-bar.service.d", "20-y.conf"),
		filepath.Join(etc, "foo-bar.service.d", "30-masked.conf"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("drop-ins:\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// The case drop-ins exist for: `systemctl edit` writes an override that
// replaces the command and adds a limit. Read without its drop-ins, the
// unit ran the packaged command; loaded directly, it now runs the
// admin's.
func TestSystemdDropinOverridesTheUnit(t *testing.T) {
	root := t.TempDir()
	etc, lib := filepath.Join(root, "etc"), filepath.Join(root, "lib")
	writeTree(t, filepath.Join(lib, "web.service"), `[Unit]
Description=packaged
[Service]
ExecStart=/usr/bin/web --packaged
User=www
`)
	writeTree(t, filepath.Join(etc, "web.service.d", "override.conf"), `[Unit]
Description=local
[Service]
ExecStart=
ExecStart=/usr/local/bin/web --local
`)

	cfg, warns, err := ConvertSystemdUnitIn(filepath.Join(lib, "web.service"), []string{etc, lib})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.command != "/usr/local/bin/web --local" || cfg.description != "local" || cfg.runAs != "www" {
		t.Errorf("command %q description %q run-as %q: the override did not apply over the unit",
			cfg.command, cfg.description, cfg.runAs)
	}
	noted := false
	for _, w := range warns {
		noted = noted || strings.Contains(w.Msg, "drop-ins applied: "+filepath.Join(etc, "web.service.d", "override.conf"))
	}
	if !noted {
		t.Errorf("no note naming the drop-in: %v", warns)
	}
	if multiple := strings.Contains(strings.Join(warnMsgs(warns), "\n"), "multiple ExecStart="); multiple {
		t.Errorf("the empty ExecStart= did not reset the packaged command: %v", warns)
	}

	// Loaded directly, through the loader's systemd directories.
	ss := service.NewServiceSet(&testConsumerLogger{})
	dl := NewDirLoader(ss, []string{filepath.Join(root, "slinit.d")})
	dl.SetSystemdDirs([]string{etc, lib})
	ss.SetLoader(dl)
	svc, err := dl.LoadService("web")
	if err != nil {
		t.Fatal(err)
	}
	if show := service.RenderShow(svc); !strings.Contains(show, "ExecStart=/usr/local/bin/web --local\n") {
		t.Errorf("loaded directly, the drop-in was not read:\n%s", show)
	}

	// Without unit directories the unit is read alone, as before.
	cfg, _, err = ConvertSystemdUnit(filepath.Join(lib, "web.service"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.command != "/usr/bin/web --packaged" {
		t.Errorf("ConvertSystemdUnit applied drop-ins: command %q", cfg.command)
	}
}

func warnMsgs(ws []SystemdWarning) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.Msg
	}
	return out
}
