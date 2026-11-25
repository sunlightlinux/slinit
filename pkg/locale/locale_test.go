package locale

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scratch points every path at a temp tree with a few keymaps, a small
// kbd-model-map and a language-fallback-map.
func scratch(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	km := filepath.Join(root, "keymaps")
	for _, f := range []string{"i386/qwerty/us.map.gz", "i386/qwerty/ro.map.gz", "i386/qwertz/de.map", "i386/qwerty/fr.map.zst"} {
		os.MkdirAll(filepath.Join(km, filepath.Dir(f)), 0o755)
		os.WriteFile(filepath.Join(km, f), []byte("x"), 0o644)
	}
	maps := filepath.Join(root, "kbd-model-map")
	os.WriteFile(maps, []byte("# keymap\tlayout\tmodel\tvariant\toptions\nus\tus\tpc105\t-\tterminate:ctrl_alt_bksp\nde\tde\tpc105\t-\t-\nro\tro\tpc105\tstd\t-\n"), 0o644)
	lang := filepath.Join(root, "language-fallback-map")
	os.WriteFile(lang, []byte("en_GB\ten_GB:en\nro_MD\tro_MD:ro\n"), 0o644)
	for p, v := range map[*string]string{
		&LocaleConfPath:  filepath.Join(root, "etc/locale.conf"),
		&VConsolePath:    filepath.Join(root, "etc/vconsole.conf"),
		&X11KeyboardPath: filepath.Join(root, "etc/X11/xorg.conf.d/00-keyboard.conf"),
		&RCConfPath:      filepath.Join(root, "etc/rc.conf"),
	} {
		old := *p
		*p = v
		t.Cleanup(func() { *p = old })
	}
	for p, v := range map[*[]string][]string{&KeymapDirs: {km}, &KbdModelMaps: {maps}, &LanguageFallbackMaps: {lang}} {
		old := *p
		*p = v
		t.Cleanup(func() { *p = old })
	}
	oldI := InstalledLocales
	InstalledLocales = func() []string {
		return []string{"C", "C.utf8", "POSIX", "en_US.utf8", "ro_RO.utf8", "de_DE.utf8", "en_GB.utf8"}
	}
	t.Cleanup(func() { InstalledLocales = oldI })
	os.MkdirAll(filepath.Join(root, "etc"), 0o755)
	return root
}

func TestParseAssignments(t *testing.T) {
	scratch(t)
	m, err := ParseAssignments([]string{"ro_RO.UTF-8"}, LocaleInstalled)
	if err != nil || m["LANG"] != "ro_RO.UTF-8" || len(m) != 1 {
		t.Errorf("bare locale: %v %v", m, err)
	}
	m, err = ParseAssignments([]string{"LANG=en_US.UTF-8", "LC_TIME=ro_RO.UTF-8", "LC_PAPER="}, LocaleInstalled)
	if err != nil || m["LC_TIME"] != "ro_RO.UTF-8" || m["LC_PAPER"] != "" {
		t.Errorf("assignments: %v %v", m, err)
	}
	for _, bad := range [][]string{
		{"fr_FR.UTF-8"},                          // not installed
		{"LANG=en_US.UTF-8", "LANG=ro_RO.UTF-8"}, // twice
		{"LC_FOO=en_US.UTF-8"},                   // not a locale variable
		{"LANG=../../etc/passwd"},                // not a locale name
		{"en_US.UTF-8", "LANG=x"},                // more than one: all must be assignments
	} {
		if _, err := ParseAssignments(bad, LocaleInstalled); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

// systemd keeps variables not given and drops those equal to LANG.
func TestMerge(t *testing.T) {
	cur := map[string]string{"LANG": "en_US.UTF-8", "LC_TIME": "ro_RO.UTF-8", "LC_PAPER": "de_DE.UTF-8"}
	got := Merge(cur, map[string]string{"LANG": "ro_RO.UTF-8", "LC_PAPER": ""})
	want := map[string]string{"LANG": "ro_RO.UTF-8", "LC_TIME": "", "LC_PAPER": ""}
	if got["LANG"] != "ro_RO.UTF-8" || got["LC_TIME"] != want["LC_TIME"] || got["LC_PAPER"] != "" || len(got) != 1 {
		t.Errorf("Merge = %v: LC_TIME equal to the new LANG must go, LC_PAPER= given empty must go", got)
	}
	got = Merge(cur, map[string]string{"LC_MESSAGES": "de_DE.UTF-8"})
	if got["LANG"] != "en_US.UTF-8" || got["LC_TIME"] != "ro_RO.UTF-8" || got["LC_MESSAGES"] != "de_DE.UTF-8" {
		t.Errorf("variables not given must stay: %v", got)
	}
}

func TestLocaleConfRoundTrip(t *testing.T) {
	scratch(t)
	os.WriteFile(LocaleConfPath, []byte("# site comment\nLANG=en_US.UTF-8\n"), 0o644)
	if err := WriteLocale(map[string]string{"LANG": "ro_RO.UTF-8", "LC_TIME": "en_GB.UTF-8"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(LocaleConfPath)
	if !strings.Contains(string(b), "# site comment") {
		t.Errorf("comment lost:\n%s", b)
	}
	m, _ := ReadLocale()
	if strings.Join(LocaleEnv(m), ",") != "LANG=ro_RO.UTF-8,LC_TIME=en_GB.UTF-8" {
		t.Errorf("read back %v", LocaleEnv(m))
	}
	os.WriteFile(LocaleConfPath, []byte("LANG=x\n"), 0o644)
	if err := WriteLocale(map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(LocaleConfPath); !os.IsNotExist(err) {
		t.Error("an empty locale must remove locale.conf")
	}
}

func TestInstalledMatchesAnyCodesetSpelling(t *testing.T) {
	scratch(t)
	for name, want := range map[string]bool{"en_US.UTF-8": true, "en_US.utf8": true, "ro_RO.Utf-8": true, "C": true, "fr_FR.UTF-8": false} {
		if got := LocaleInstalled(name); got != want {
			t.Errorf("LocaleInstalled(%q) = %v", name, got)
		}
	}
}

func TestVConsoleKeepsOtherKeys(t *testing.T) {
	scratch(t)
	os.WriteFile(VConsolePath, []byte("FONT=ter-v16n\nKEYMAP=us\n"), 0o644)
	if err := WriteVConsole(VConsole{Keymap: "de"}, X11{Layout: "de", Model: "pc105"}); err != nil {
		t.Fatal(err)
	}
	vc, x, _ := ReadVConsole()
	b, _ := os.ReadFile(VConsolePath)
	if vc.Keymap != "de" || x.Layout != "de" || !strings.Contains(string(b), "FONT=ter-v16n") {
		t.Errorf("vconsole.conf:\n%s", b)
	}
}

func TestXorgKeyboardRoundTrip(t *testing.T) {
	scratch(t)
	x := X11{Layout: "us,ro", Model: "pc105", Variant: ",std", Options: "grp:alt_shift_toggle"}
	if err := WriteXorgKeyboard(x); err != nil {
		t.Fatal(err)
	}
	if got := ReadXorgKeyboard(); got != x {
		t.Errorf("read back %+v", got)
	}
	// vconsole.conf's copy wins when it names a layout.
	os.WriteFile(VConsolePath, []byte("XKBLAYOUT=de\n"), 0o644)
	if CurrentX11().Layout != "de" {
		t.Error("vconsole.conf's XKBLAYOUT must win over 00-keyboard.conf")
	}
	if err := WriteXorgKeyboard(X11{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(X11KeyboardPath); !os.IsNotExist(err) {
		t.Error("an empty layout must remove 00-keyboard.conf")
	}
}

func TestKeymaps(t *testing.T) {
	scratch(t)
	for name, want := range map[string]bool{"us": true, "de": true, "fr": true, "xx": false, "../us": false, "": false} {
		if got := KeymapExists(name); got != want {
			t.Errorf("KeymapExists(%q) = %v", name, got)
		}
	}
	if got := strings.Join(ListKeymaps(), ","); got != "de,fr,ro,us" {
		t.Errorf("ListKeymaps = %s", got)
	}
}

func TestConversion(t *testing.T) {
	scratch(t)
	if x := X11FromKeymap("us"); x.Layout != "us" || x.Model != "pc105" || x.Variant != "" || x.Options != "terminate:ctrl_alt_bksp" {
		t.Errorf("X11FromKeymap(us) = %+v", x)
	}
	if km := KeymapFromX11(X11{Layout: "ro", Variant: "std"}); km != "ro" {
		t.Errorf("layout+variant: %q", km)
	}
	if km := KeymapFromX11(X11{Layout: "de", Variant: "nodeadkeys"}); km != "de" {
		t.Errorf("layout alone as the fallback: %q", km)
	}
	if km := KeymapFromX11(X11{Layout: "jp"}); km != "" {
		t.Errorf("no row: %q", km)
	}
	KbdModelMaps = []string{"/nonexistent"}
	if !X11FromKeymap("us").Empty() {
		t.Error("without a map nothing converts")
	}
	if l := LanguageFallback("en_GB.UTF-8"); l != "en_GB:en" {
		t.Errorf("LanguageFallback = %q", l)
	}
}

func TestRCConfKeymap(t *testing.T) {
	scratch(t)
	os.WriteFile(RCConfPath, []byte("HARDWARECLOCK=UTC\nKEYMAP=\"us\"\n"), 0o644)
	if err := SetRCConfKeymap("de"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(RCConfPath); !strings.Contains(string(b), "KEYMAP=de") || !strings.Contains(string(b), "HARDWARECLOCK=UTC") {
		t.Errorf("rc.conf:\n%s", b)
	}
	orig := "#KEYMAP=us\n"
	os.WriteFile(RCConfPath, []byte(orig), 0o644)
	SetRCConfKeymap("de")
	if b, _ := os.ReadFile(RCConfPath); string(b) != orig {
		t.Errorf("rc.conf without an active KEYMAP changed:\n%s", b)
	}
}

func TestSafeX11(t *testing.T) {
	if !SafeX11(X11{Layout: "us,ro", Options: "grp:alt_shift_toggle"}) {
		t.Error("ordinary layout refused")
	}
	for _, bad := range []X11{{Layout: "us\"; rm"}, {Options: "a\\b"}, {Model: "x\ny"}} {
		if SafeX11(bad) {
			t.Errorf("%+v accepted", bad)
		}
	}
}

// LANGUAGE was the one variable not validated, so a newline in it wrote a
// second assignment into locale.conf: LANGUAGE=en\nLANG=/tmp/evil came
// back as LANG=/tmp/evil. systemd validates every variable. LANGUAGE is a
// colon-separated priority list, so each item must be a locale name; it
// need not be installed ("de" never is).
func TestLanguageIsValidated(t *testing.T) {
	scratch(t)
	for _, ok := range []string{"LANGUAGE=de_DE:de:en", "LANGUAGE=en", "LANGUAGE="} {
		if _, err := ParseAssignments([]string{ok}, LocaleInstalled); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"LANGUAGE=en\nLANG=/tmp/evil", "LANGUAGE=../../x", "LANGUAGE=de::en", "LANGUAGE=en\r"} {
		m, err := ParseAssignments([]string{bad}, LocaleInstalled)
		if err == nil {
			t.Errorf("%q accepted", bad)
			if WriteLocale(m) == nil {
				got, _ := ReadLocale()
				t.Errorf("  and locale.conf now reads %v", LocaleEnv(got))
			}
		}
	}
}
