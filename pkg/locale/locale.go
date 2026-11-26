// Package locale holds what slinit-localectl and slinit-localed share: the
// system locale (/etc/locale.conf), the console keymap (/etc/vconsole.conf)
// and the X11 keyboard layout (/etc/vconsole.conf's XKB* keys and
// /etc/X11/xorg.conf.d/00-keyboard.conf), with systemd-localed's rules for
// reading, validating and writing them. The paths are variables so tests
// can point them at a scratch tree.
package locale

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/hostname"
)

var (
	LocaleConfPath  = "/etc/locale.conf"
	VConsolePath    = "/etc/vconsole.conf"
	X11KeyboardPath = "/etc/X11/xorg.conf.d/00-keyboard.conf"
	// RCConfPath is Void's /etc/rc.conf, whose KEYMAP= slinit-services'
	// boot script loads on every boot.
	RCConfPath = "/etc/rc.conf"
	KeymapDirs = []string{"/usr/share/keymaps", "/usr/share/kbd/keymaps", "/usr/lib/kbd/keymaps"}
	// KbdModelMaps and LanguageFallbackMaps are systemd's data files for
	// converting between console keymaps and X11 layouts and for filling
	// LANGUAGE from LANG. Sunlight does not ship them; without one, no
	// conversion is found, as systemd behaves when nothing matches.
	KbdModelMaps         = []string{"/usr/share/slinit/kbd-model-map", "/usr/share/systemd/kbd-model-map"}
	LanguageFallbackMaps = []string{"/usr/share/slinit/language-fallback-map", "/usr/share/systemd/language-fallback-map"}
	// LocaleBin lists the installed locales (locale -a).
	LocaleBin = "locale"
)

// Variables are the locale.conf variables systemd knows, in its order.
var Variables = []string{
	"LANG", "LANGUAGE", "LC_CTYPE", "LC_NUMERIC", "LC_TIME", "LC_COLLATE",
	"LC_MONETARY", "LC_MESSAGES", "LC_PAPER", "LC_NAME", "LC_ADDRESS",
	"LC_TELEPHONE", "LC_MEASUREMENT", "LC_IDENTIFICATION",
}

func isVariable(name string) bool {
	for _, v := range Variables {
		if v == name {
			return true
		}
	}
	return false
}

// ReadLocale returns the variables set in /etc/locale.conf.
func ReadLocale() (map[string]string, error) {
	f, err := hostname.LoadEnvFile(LocaleConfPath)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, v := range Variables {
		if x := f.Get(v); x != "" {
			out[v] = x
		}
	}
	return out, nil
}

// LocaleEnv renders variables as systemd's Locale property does: VAR=value
// in systemd's order, set ones only.
func LocaleEnv(m map[string]string) []string {
	out := []string{}
	for _, v := range Variables {
		if x := m[v]; x != "" {
			out = append(out, v+"="+x)
		}
	}
	return out
}

// WriteLocale replaces /etc/locale.conf's variables with m, keeping any
// other line. An empty result removes the file.
func WriteLocale(m map[string]string) error {
	f, err := hostname.LoadEnvFile(LocaleConfPath)
	if err != nil {
		return err
	}
	for _, v := range Variables {
		f.Set(v, m[v])
	}
	if len(f.Pairs()) == 0 {
		if err := os.Remove(LocaleConfPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return f.Save()
}

var localeRE = regexp.MustCompile(`^[A-Za-z0-9_.@-]+$`)

// ValidLocale checks a locale name the way systemd's locale_is_valid
// does in effect: a non-empty, safe file name (a locale names a directory)
// of letters, digits and "_.@-", not starting with '.'.
func ValidLocale(name string) bool {
	return name != "" && len(name) < 128 && localeRE.MatchString(name) &&
		!strings.HasPrefix(name, ".") && name != "." && name != ".."
}

// normalizeCodeset makes "en_US.UTF-8" and "en_US.utf8" compare equal,
// as glibc does when it looks a locale up.
func normalizeCodeset(name string) string {
	dot := strings.IndexByte(name, '.')
	if dot < 0 {
		return name
	}
	cs := name[dot+1:]
	mod := ""
	if at := strings.IndexByte(cs, '@'); at >= 0 {
		cs, mod = cs[:at], cs[at:]
	}
	cs = strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(cs))
	return name[:dot+1] + cs + mod
}

// InstalledLocales lists `locale -a`; nil when that cannot be run.
var InstalledLocales = func() []string {
	out, err := exec.Command(LocaleBin, "-a").Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}

// LocaleInstalled reports whether name is installed. "C" and "POSIX"
// always are. When the list cannot be had, it says yes rather than refuse
// every locale.
func LocaleInstalled(name string) bool {
	if name == "C" || name == "POSIX" || strings.HasPrefix(name, "C.") {
		return true
	}
	all := InstalledLocales()
	if all == nil {
		return true
	}
	want := normalizeCodeset(name)
	for _, l := range all {
		if normalizeCodeset(l) == want {
			return true
		}
	}
	return false
}

// ParseAssignments turns SetLocale's argument into variables: a single
// bare locale means LANG=; otherwise every item is VAR=value with VAR one
// of Variables, each at most once. installed checks each value.
func ParseAssignments(items []string, installed func(string) bool) (map[string]string, error) {
	out := map[string]string{}
	if len(items) == 1 && !strings.Contains(items[0], "=") {
		if !ValidLocale(items[0]) {
			return nil, fmt.Errorf("Invalid locale specification: %s", items[0])
		}
		if !installed(items[0]) {
			return nil, fmt.Errorf("Specified locale is not installed: %s", items[0])
		}
		out["LANG"] = items[0]
		return out, nil
	}
	for _, it := range items {
		eq := strings.IndexByte(it, '=')
		if eq <= 0 || !isVariable(it[:eq]) {
			return nil, fmt.Errorf("Locale assignment %s not valid, refusing.", it)
		}
		name, val := it[:eq], it[eq+1:]
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("Locale variable %s set twice, refusing.", name)
		}
		if val != "" && name == "LANGUAGE" {
			// A priority list of languages, not one locale: each
			// item must be a locale name, none need be installed.
			for _, l := range strings.Split(val, ":") {
				if !ValidLocale(l) {
					return nil, fmt.Errorf("Locale %s is not valid, refusing.", val)
				}
			}
		} else if val != "" {
			if !ValidLocale(val) {
				return nil, fmt.Errorf("Locale %s is not valid, refusing.", val)
			}
			if !installed(val) {
				return nil, fmt.Errorf("Locale %s not installed, refusing.", val)
			}
		}
		out[name] = val
	}
	return out, nil
}

// Merge applies systemd's rules for SetLocale: a variable not given keeps
// its current value, then every variable equal to LANG is dropped.
func Merge(cur, next map[string]string) map[string]string {
	out := map[string]string{}
	for _, v := range Variables {
		if x := next[v]; x != "" {
			out[v] = x
		} else if x := cur[v]; x != "" {
			if _, given := next[v]; !given {
				out[v] = x
			}
		}
	}
	for _, v := range Variables {
		if v != "LANG" && out[v] != "" && out[v] == out["LANG"] {
			delete(out, v)
		}
	}
	return out
}

// LanguageFallback looks LANG up in systemd's language-fallback-map and
// returns the LANGUAGE value to set with it, or "".
func LanguageFallback(lang string) string {
	base := lang
	if i := strings.IndexAny(base, ".@"); i >= 0 {
		base = base[:i]
	}
	for _, rows := range readMaps(LanguageFallbackMaps) {
		if len(rows) >= 2 && rows[0] == base {
			return rows[1]
		}
	}
	return ""
}

// VConsole is the console keymap; X11 the X11 keyboard layout.
type VConsole struct{ Keymap, Toggle string }
type X11 struct{ Layout, Model, Variant, Options string }

func (x X11) Empty() bool { return x == X11{} }

// ReadVConsole reads the keymap and the X11 layout systemd keeps in
// vconsole.conf.
func ReadVConsole() (VConsole, X11, error) {
	f, err := hostname.LoadEnvFile(VConsolePath)
	if err != nil {
		return VConsole{}, X11{}, err
	}
	return VConsole{f.Get("KEYMAP"), f.Get("KEYMAP_TOGGLE")},
		X11{f.Get("XKBLAYOUT"), f.Get("XKBMODEL"), f.Get("XKBVARIANT"), f.Get("XKBOPTIONS")}, nil
}

var xkbOption = regexp.MustCompile(`^\s*Option\s+"(Xkb(?:Layout|Model|Variant|Options))"\s+"([^"]*)"`)

// ReadXorgKeyboard reads the layout from 00-keyboard.conf.
func ReadXorgKeyboard() X11 {
	f, err := os.Open(X11KeyboardPath)
	if err != nil {
		return X11{}
	}
	defer f.Close()
	var x X11
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		m := xkbOption.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		switch m[1] {
		case "XkbLayout":
			x.Layout = m[2]
		case "XkbModel":
			x.Model = m[2]
		case "XkbVariant":
			x.Variant = m[2]
		case "XkbOptions":
			x.Options = m[2]
		}
	}
	return x
}

// CurrentX11 is the layout systemd reports: vconsole.conf's when it names
// one, else 00-keyboard.conf's.
func CurrentX11() X11 {
	if _, x, err := ReadVConsole(); err == nil && !x.Empty() {
		return x
	}
	return ReadXorgKeyboard()
}

// WriteVConsole sets the keymap and the X11 layout in vconsole.conf,
// keeping its other lines (FONT=...). An empty file is removed.
func WriteVConsole(vc VConsole, x X11) error {
	f, err := hostname.LoadEnvFile(VConsolePath)
	if err != nil {
		return err
	}
	for k, v := range map[string]string{"KEYMAP": vc.Keymap, "KEYMAP_TOGGLE": vc.Toggle,
		"XKBLAYOUT": x.Layout, "XKBMODEL": x.Model, "XKBVARIANT": x.Variant, "XKBOPTIONS": x.Options} {
		f.Set(k, v)
	}
	if len(f.Pairs()) == 0 {
		if err := os.Remove(VConsolePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return f.Save()
}

// WriteXorgKeyboard writes 00-keyboard.conf as systemd-localed does, or
// removes it for an empty layout.
func WriteXorgKeyboard(x X11) error {
	if x.Empty() {
		if err := os.Remove(X11KeyboardPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	var b strings.Builder
	b.WriteString("# Written by slinit-localed(8), read by slinit-localed and Xorg. It's\n" +
		"# probably wise not to edit this file manually. Use slinit-localectl(1) to\n" +
		"# update this file.\n" +
		"Section \"InputClass\"\n" +
		"        Identifier \"system-keyboard\"\n" +
		"        MatchIsKeyboard \"on\"\n")
	for _, o := range [][2]string{{"XkbLayout", x.Layout}, {"XkbModel", x.Model}, {"XkbVariant", x.Variant}, {"XkbOptions", x.Options}} {
		if o[1] != "" {
			fmt.Fprintf(&b, "        Option \"%s\" \"%s\"\n", o[0], o[1])
		}
	}
	b.WriteString("EndSection\n")
	if err := os.MkdirAll(filepath.Dir(X11KeyboardPath), 0o755); err != nil {
		return err
	}
	return hostname.WriteAtomic(X11KeyboardPath, []byte(b.String()), 0o644)
}

// SetRCConfKeymap rewrites an uncommented KEYMAP= in /etc/rc.conf, which
// the boot script loads on every boot; an rc.conf without one is left
// alone.
func SetRCConfKeymap(keymap string) error {
	f, err := hostname.LoadShellEnvFile(RCConfPath)
	if err != nil {
		return err
	}
	if f.Get("KEYMAP") == "" || f.Get("KEYMAP") == keymap || keymap == "" {
		return nil
	}
	f.Set("KEYMAP", keymap)
	return f.Save()
}

// ValidKeymapName is systemd's keymap_is_valid: a safe file name under
// 128 bytes.
func ValidKeymapName(name string) bool {
	return len(name) < 128 && hostname.SafeString(name, true)
}

// KeymapExists looks for NAME.map (optionally compressed) under the keymap
// directories, as systemd's keymap_exists.
func KeymapExists(name string) bool {
	if !ValidKeymapName(name) {
		return false
	}
	found := false
	for _, dir := range KeymapDirs {
		filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
			if err != nil || found {
				return filepath.SkipDir
			}
			if !d.IsDir() {
				for _, ext := range []string{".map", ".map.gz", ".map.bz2", ".map.xz", ".map.zst"} {
					if d.Name() == name+ext {
						found = true
						return filepath.SkipAll
					}
				}
			}
			return nil
		})
		if found {
			return true
		}
	}
	return false
}

// ListKeymaps lists the installed console keymaps.
func ListKeymaps() []string {
	seen := map[string]bool{}
	for _, dir := range KeymapDirs {
		filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				n := d.Name()
				for _, ext := range []string{".map.gz", ".map.bz2", ".map.xz", ".map.zst", ".map"} {
					if strings.HasSuffix(n, ext) {
						seen[strings.TrimSuffix(n, ext)] = true
						break
					}
				}
			}
			return nil
		})
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SafeX11 is systemd's x11_context_is_safe: each field, when set, is a
// string_is_safe string without quotes or backslashes.
func SafeX11(x X11) bool {
	for _, v := range []string{x.Layout, x.Model, x.Variant, x.Options} {
		if v != "" && !hostname.SafeString(v, false) {
			return false
		}
		if strings.ContainsAny(v, "\"'\\") {
			return false
		}
	}
	return true
}

func readMaps(paths []string) [][]string {
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		var rows [][]string
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			l := strings.TrimSpace(sc.Text())
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			rows = append(rows, strings.Fields(l))
		}
		f.Close()
		return rows
	}
	return nil
}

func dash(s string) string {
	if s == "-" {
		return ""
	}
	return s
}

// X11FromKeymap converts a console keymap through kbd-model-map (keymap,
// layout, model, variant, options per row); the zero X11 when nothing
// matches or there is no map.
func X11FromKeymap(keymap string) X11 {
	for _, r := range readMaps(KbdModelMaps) {
		if len(r) >= 5 && r[0] == keymap {
			return X11{Layout: dash(r[1]), Model: dash(r[2]), Variant: dash(r[3]), Options: dash(r[4])}
		}
	}
	return X11{}
}

// KeymapFromX11 converts an X11 layout back: a row matching layout and
// variant first, else the first matching the layout alone.
func KeymapFromX11(x X11) string {
	rows := readMaps(KbdModelMaps)
	for _, r := range rows {
		if len(r) >= 5 && r[1] == x.Layout && dash(r[3]) == x.Variant {
			return r[0]
		}
	}
	for _, r := range rows {
		if len(r) >= 5 && r[1] == x.Layout {
			return r[0]
		}
	}
	return ""
}

// VConsolePlan is what setting the console keymap writes, worked out from
// the files as they are.
type VConsolePlan struct {
	VC           VConsole
	X            X11
	XNeedsUpdate bool // the X11 layout changes too (or its two copies are brought in step)
	Change       bool // anything to do at all
	Note         string
}

// PlanVConsole works out setting the console keymap. With convert, the
// X11 layout follows through kbd-model-map when it has a row for the
// keymap; without, the two copies of the X11 layout are still brought in
// step, as systemd-localed does.
func PlanVConsole(keymap, toggle string, convert bool) (VConsolePlan, error) {
	curVC, vcX, err := ReadVConsole()
	if err != nil {
		return VConsolePlan{}, err
	}
	xorgX := ReadXorgKeyboard()
	p := VConsolePlan{VC: VConsole{Keymap: keymap, Toggle: toggle}, X: CurrentX11()}
	if convert {
		if c := X11FromKeymap(keymap); !c.Empty() {
			p.X, p.XNeedsUpdate = c, c != vcX || c != xorgX
			p.Note = fmt.Sprintf("The virtual console keymap '%s' is converted to X11 keyboard layout '%s' model '%s' variant '%s' options '%s'",
				keymap, c.Layout, c.Model, c.Variant, c.Options)
		} else {
			p.Note = fmt.Sprintf("No conversion found for virtual console keymap \"%s\".", keymap)
		}
	} else {
		p.XNeedsUpdate = vcX != xorgX
	}
	p.Change = p.VC != curVC || p.XNeedsUpdate
	return p, nil
}

// Apply writes the plan: vconsole.conf, 00-keyboard.conf when the layout
// changes, and rc.conf's KEYMAP=. Failing to write vconsole.conf or
// rc.conf is reported through warn and does not stop the rest, as
// systemd-localed carries on past a failed vconsole.conf.
func (p VConsolePlan) Apply(warn func(string, ...any)) error {
	if err := WriteVConsole(p.VC, p.X); err != nil {
		warn("Failed to write virtual console keymap, ignoring: %v", err)
	}
	if p.XNeedsUpdate {
		if err := WriteXorgKeyboard(p.X); err != nil {
			warn("Failed to write X11 keyboard layout, ignoring: %v", err)
		}
	}
	if err := SetRCConfKeymap(p.VC.Keymap); err != nil {
		warn("Failed to update KEYMAP in %s, ignoring: %v", RCConfPath, err)
	}
	return nil
}

// X11Plan is what setting the X11 layout writes.
type X11Plan struct {
	In       X11
	VC       VConsole
	VCChange bool
	Change   bool
	Note     string
}

// PlanX11 works out setting the X11 layout. With convert, the console
// keymap follows through kbd-model-map: a row matching the layout and
// variant, else one matching the layout.
func PlanX11(in X11, convert bool) (X11Plan, error) {
	curVC, vcX, err := ReadVConsole()
	if err != nil {
		return X11Plan{}, err
	}
	xorgX := ReadXorgKeyboard()
	p := X11Plan{In: in, VC: curVC}
	if convert {
		if km := KeymapFromX11(in); km != "" {
			p.VC = VConsole{Keymap: km}
			p.VCChange = p.VC != curVC
			p.Note = fmt.Sprintf("The X11 keyboard layout '%s' is converted to virtual console keymap '%s'", in.Layout, km)
		} else {
			p.Note = fmt.Sprintf("No conversion to virtual console map found for \"%s\".", in.Layout)
		}
	}
	p.Change = in != vcX || in != xorgX || p.VCChange
	return p, nil
}

// Apply writes the plan: both copies of the layout, and the console
// keymap when it changes.
func (p X11Plan) Apply(warn func(string, ...any)) error {
	if err := WriteVConsole(p.VC, p.In); err != nil {
		warn("Failed to write virtual console keymap, ignoring: %v", err)
	}
	if err := WriteXorgKeyboard(p.In); err != nil {
		return fmt.Errorf("Failed to set X11 keyboard layout: %w", err)
	}
	if p.VCChange {
		if err := SetRCConfKeymap(p.VC.Keymap); err != nil {
			warn("Failed to update KEYMAP in %s, ignoring: %v", RCConfPath, err)
		}
	}
	return nil
}

// PlanLocale works out SetLocale's result: the parsed items, LANGUAGE from
// the fallback map when LANG is given without it, merged with the current
// settings. change is false when nothing would differ.
func PlanLocale(items []string) (merged map[string]string, change bool, err error) {
	next, err := ParseAssignments(items, LocaleInstalled)
	if err != nil {
		return nil, false, err
	}
	if next["LANG"] != "" && next["LANGUAGE"] == "" {
		if l := LanguageFallback(next["LANG"]); l != "" {
			next["LANGUAGE"] = l
		}
	}
	cur, err := ReadLocale()
	if err != nil {
		return nil, false, err
	}
	merged = Merge(cur, next)
	if len(merged) != len(cur) {
		return merged, true, nil
	}
	for k, v := range merged {
		if cur[k] != v {
			return merged, true, nil
		}
	}
	return merged, false, nil
}
