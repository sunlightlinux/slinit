package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/sunlightlinux/slinit/pkg/locale"
	"github.com/sunlightlinux/slinit/pkg/polkit"
)

type world struct {
	t      *testing.T
	root   string
	s      *server
	allow  bool
	asked  []string
	loaded []string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{t: t, root: t.TempDir(), allow: true}
	km := filepath.Join(w.root, "keymaps/i386/qwerty")
	os.MkdirAll(km, 0o755)
	for _, k := range []string{"us", "de", "ro"} {
		os.WriteFile(filepath.Join(km, k+".map.gz"), []byte("x"), 0o644)
	}
	maps := filepath.Join(w.root, "kbd-model-map")
	os.WriteFile(maps, []byte("us\tus\tpc105\t-\t-\nde\tde\tpc105\t-\t-\nro\tro\tpc105\tstd\t-\n"), 0o644)
	os.MkdirAll(filepath.Join(w.root, "etc"), 0o755)
	for p, v := range map[*string]string{
		&locale.LocaleConfPath:  filepath.Join(w.root, "etc/locale.conf"),
		&locale.VConsolePath:    filepath.Join(w.root, "etc/vconsole.conf"),
		&locale.X11KeyboardPath: filepath.Join(w.root, "etc/X11/xorg.conf.d/00-keyboard.conf"),
		&locale.RCConfPath:      filepath.Join(w.root, "etc/rc.conf"),
	} {
		old := *p
		*p = v
		t.Cleanup(func() { *p = old })
	}
	for p, v := range map[*[]string][]string{
		&locale.KeymapDirs:           {filepath.Join(w.root, "keymaps")},
		&locale.KbdModelMaps:         {maps},
		&locale.LanguageFallbackMaps: {filepath.Join(w.root, "none")},
	} {
		old := *p
		*p = v
		t.Cleanup(func() { *p = old })
	}
	oldI := locale.InstalledLocales
	locale.InstalledLocales = func() []string { return []string{"C", "en_US.utf8", "ro_RO.utf8", "de_DE.utf8"} }
	t.Cleanup(func() { locale.InstalledLocales = oldI })

	w.s = newServer(nil, &polkit.Checker{
		UID: func(dbus.Sender) (uint32, error) { return 1000, nil },
		Check: func(_ dbus.Sender, action string, _ map[string]string, _ bool) (bool, bool, error) {
			w.asked = append(w.asked, action)
			return w.allow, !w.allow, nil
		},
	})
	w.s.loadKeymap = func(k string) { w.loaded = append(w.loaded, k) }
	return w
}

func errName(e *dbus.Error) string {
	if e == nil {
		return ""
	}
	return e.Name
}

func prop(t *testing.T, s *server, n string) any {
	t.Helper()
	v, ok := s.property(n)
	if !ok {
		t.Fatalf("no property %s", n)
	}
	return v.Value()
}

func TestSetLocale(t *testing.T) {
	w := newWorld(t)
	if got := errName(w.s.SetLocale(":1.9", []string{"fr_FR.UTF-8"}, true)); got != "org.freedesktop.DBus.Error.InvalidArgs" {
		t.Errorf("not installed: %q", got)
	}
	w.allow = false
	if errName(w.s.SetLocale(":1.9", []string{"ro_RO.UTF-8"}, true)) == "" {
		t.Fatal("refused request succeeded")
	}
	if _, err := os.Stat(locale.LocaleConfPath); !os.IsNotExist(err) {
		t.Error("a refused request wrote locale.conf")
	}
	w.allow = true
	if err := w.s.SetLocale(":1.9", []string{"LANG=en_US.UTF-8", "LC_TIME=ro_RO.UTF-8"}, true); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(prop(t, w.s, "Locale").([]string), ","); got != "LANG=en_US.UTF-8,LC_TIME=ro_RO.UTF-8" {
		t.Errorf("Locale = %s", got)
	}
	// A later LANG keeps LC_TIME, unless it now equals LANG.
	if err := w.s.SetLocale(":1.9", []string{"ro_RO.UTF-8"}, true); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(prop(t, w.s, "Locale").([]string), ","); got != "LANG=ro_RO.UTF-8" {
		t.Errorf("Locale = %s, want LC_TIME dropped as equal to LANG", got)
	}
	n := len(w.asked)
	if err := w.s.SetLocale(":1.9", []string{"ro_RO.UTF-8"}, true); err != nil || len(w.asked) != n {
		t.Error("no change must not ask polkit")
	}
	if w.asked[0] != iface+".set-locale" {
		t.Errorf("asked %v", w.asked)
	}
}

func TestSetVConsoleKeyboard(t *testing.T) {
	w := newWorld(t)
	os.WriteFile(locale.RCConfPath, []byte("KEYMAP=us\n"), 0o644)
	os.WriteFile(locale.VConsolePath, []byte("FONT=ter-v16n\nKEYMAP=us\n"), 0o644)
	if got := errName(w.s.SetVConsoleKeyboard(":1.9", "xx", "", false, true)); got != "org.freedesktop.DBus.Error.Failed" {
		t.Errorf("uninstalled keymap: %q", got)
	}
	if err := w.s.SetVConsoleKeyboard(":1.9", "de", "", false, true); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(locale.VConsolePath)
	rc, _ := os.ReadFile(locale.RCConfPath)
	if prop(t, w.s, "VConsoleKeymap") != "de" || !strings.Contains(string(b), "FONT=ter-v16n") || !strings.Contains(string(rc), "KEYMAP=de") {
		t.Errorf("vconsole:\n%s\nrc.conf:\n%s", b, rc)
	}
	if len(w.loaded) != 1 || w.loaded[0] != "de" {
		t.Errorf("keymap not applied now: %v", w.loaded)
	}
	if _, err := os.Stat(locale.X11KeyboardPath); !os.IsNotExist(err) {
		t.Error("without convert the X11 layout must stay untouched")
	}
	// With convert, the X11 layout follows through kbd-model-map.
	if err := w.s.SetVConsoleKeyboard(":1.9", "ro", "", true, true); err != nil {
		t.Fatal(err)
	}
	if prop(t, w.s, "X11Layout") != "ro" || prop(t, w.s, "X11Variant") != "std" || locale.ReadXorgKeyboard().Layout != "ro" {
		t.Errorf("converted X11 = %v/%v", prop(t, w.s, "X11Layout"), prop(t, w.s, "X11Variant"))
	}
	if w.asked[len(w.asked)-1] != iface+".set-keyboard" {
		t.Errorf("asked %v", w.asked)
	}
}

func TestSetX11Keyboard(t *testing.T) {
	w := newWorld(t)
	if got := errName(w.s.SetX11Keyboard(":1.9", `us"`, "", "", "", false, true)); got != "org.freedesktop.DBus.Error.InvalidArgs" {
		t.Errorf("unsafe layout: %q", got)
	}
	if err := w.s.SetX11Keyboard(":1.9", "us,ro", "pc105", ",std", "grp:alt_shift_toggle", false, true); err != nil {
		t.Fatal(err)
	}
	if x := locale.ReadXorgKeyboard(); x.Layout != "us,ro" || x.Options != "grp:alt_shift_toggle" {
		t.Errorf("00-keyboard.conf: %+v", x)
	}
	if b, _ := os.ReadFile(locale.VConsolePath); !strings.Contains(string(b), "XKBLAYOUT=us,ro") && !strings.Contains(string(b), `XKBLAYOUT="us,ro"`) {
		t.Errorf("vconsole.conf must carry the X11 layout too:\n%s", b)
	}
	if prop(t, w.s, "VConsoleKeymap") != "" || len(w.loaded) != 0 {
		t.Error("without convert the console keymap must stay")
	}
	n := len(w.asked)
	if err := w.s.SetX11Keyboard(":1.9", "us,ro", "pc105", ",std", "grp:alt_shift_toggle", false, true); err != nil || len(w.asked) != n {
		t.Error("no change must not ask polkit")
	}
	if err := w.s.SetX11Keyboard(":1.9", "de", "pc105", "", "", true, true); err != nil {
		t.Fatal(err)
	}
	if prop(t, w.s, "VConsoleKeymap") != "de" || len(w.loaded) != 1 {
		t.Errorf("convert: keymap %v loaded %v", prop(t, w.s, "VConsoleKeymap"), w.loaded)
	}
}

func TestPolicyDeclaresEveryActionAsked(t *testing.T) {
	b, err := os.ReadFile("../../data/polkit-1/actions/org.freedesktop.locale1.policy")
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, m := range regexp.MustCompile(`<action id="([^"]+)">`).FindAllStringSubmatch(string(b), -1) {
		declared[m[1]] = true
	}
	w := newWorld(t)
	w.s.SetLocale(":1.9", []string{"de_DE.UTF-8"}, true)
	w.s.SetX11Keyboard(":1.9", "de", "", "", "", false, true)
	asked := map[string]bool{}
	for _, a := range w.asked {
		asked[a] = true
		if !declared[a] {
			t.Errorf("daemon asks %s, which the policy does not declare", a)
		}
	}
	for a := range declared {
		if !asked[a] {
			t.Errorf("policy declares %s, which the daemon never asks", a)
		}
	}
}
