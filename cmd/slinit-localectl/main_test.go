package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/locale"
)

func scratch(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "etc"), 0o755)
	for p, v := range map[*string]string{
		&locale.LocaleConfPath:  filepath.Join(root, "etc/locale.conf"),
		&locale.VConsolePath:    filepath.Join(root, "etc/vconsole.conf"),
		&locale.X11KeyboardPath: filepath.Join(root, "etc/00-keyboard.conf"),
		&locale.RCConfPath:      filepath.Join(root, "etc/rc.conf"),
		&xkbRules:               filepath.Join(root, "base.lst"),
	} {
		old := *p
		*p = v
		t.Cleanup(func() { *p = old })
	}
	oldI := locale.InstalledLocales
	locale.InstalledLocales = func() []string { return []string{"C", "en_US.utf8", "ro_RO.utf8"} }
	t.Cleanup(func() { locale.InstalledLocales = oldI })
	oldL := locale.LanguageFallbackMaps
	locale.LanguageFallbackMaps = nil
	t.Cleanup(func() { locale.LanguageFallbackMaps = oldL })
	return root
}

func TestParseArgs(t *testing.T) {
	o, err := parseArgs(nil)
	if err != nil || o.cmd != "status" {
		t.Errorf("default: %+v %v", o, err)
	}
	o, _ = parseArgs([]string{"--no-convert", "set-keymap", "de"})
	if o.cmd != "set-keymap" || !o.noConvert || o.args[0] != "de" {
		t.Errorf("%+v", o)
	}
	if _, err := parseArgs([]string{"-H", "host"}); err == nil {
		t.Error("--host must be refused")
	}
	if _, err := parseArgs([]string{"--bogus"}); err == nil {
		t.Error("an unknown flag must be refused")
	}
}

func TestSetLocaleAndStatus(t *testing.T) {
	scratch(t)
	if err := dispatch(&bytes.Buffer{}, options{cmd: "set-locale", args: []string{"LANG=ro_RO.UTF-8", "LC_TIME=en_US.UTF-8"}}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(locale.VConsolePath, []byte("KEYMAP=ro\nXKBLAYOUT=ro\nXKBVARIANT=std\n"), 0o644)
	var out bytes.Buffer
	if err := dispatch(&out, options{cmd: "status"}); err != nil {
		t.Fatal(err)
	}
	want := "System Locale: LANG=ro_RO.UTF-8\n               LC_TIME=en_US.UTF-8\n    VC Keymap: ro\n   X11 Layout: ro\n  X11 Variant: std\n"
	if out.String() != want {
		t.Errorf("status:\n%s\nwant:\n%s", out.String(), want)
	}
	if err := dispatch(&bytes.Buffer{}, options{cmd: "set-locale", args: []string{"fr_FR.UTF-8"}}); err == nil {
		t.Error("an uninstalled locale must be refused")
	}
}

func TestListRules(t *testing.T) {
	root := scratch(t)
	os.WriteFile(filepath.Join(root, "base.lst"), []byte(`! model
  pc105           Generic 105-key PC
  pc104           Generic 104-key PC

! layout
  us              English (US)
  ro              Romanian

! variant
  std             ro: Romanian (standard)
  winkeys         ro: Romanian (Windows)
  intl            us: English (US, intl.)

! option
  grp                  Switching to another layout
  grp:alt_shift_toggle Alt+Shift
`), 0o644)
	for _, c := range []struct {
		cmd  string
		args []string
		want string
	}{
		{"list-x11-keymap-models", nil, "pc104,pc105"},
		{"list-x11-keymap-layouts", nil, "ro,us"},
		{"list-x11-keymap-variants", []string{"ro"}, "std,winkeys"},
		{"list-x11-keymap-variants", nil, "intl,std,winkeys"},
		{"list-x11-keymap-options", nil, "grp:alt_shift_toggle"},
	} {
		var out bytes.Buffer
		if err := dispatch(&out, options{cmd: c.cmd, args: c.args}); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(strings.Fields(out.String()), ","); got != c.want {
			t.Errorf("%s %v = %s, want %s", c.cmd, c.args, got, c.want)
		}
	}
}
