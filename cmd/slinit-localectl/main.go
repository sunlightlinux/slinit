// slinit-localectl — systemd localectl(1) parity, D-Bus-free.
//
// Reads and writes the system locale and keyboard settings directly,
// through the same code slinit-localed serves on the bus (pkg/locale):
// /etc/locale.conf, /etc/vconsole.conf (KEYMAP and the XKB* keys),
// /etc/X11/xorg.conf.d/00-keyboard.conf, and /etc/rc.conf's KEYMAP= when
// it is set. Like slinit-hostnamectl and slinit-timedatectl it needs no
// bus, so it also works in a rescue shell.
//
// Commands:
//
//	status                                 show the current settings
//	set-locale LOCALE...                   set LANG, or VAR=value items
//	list-locales                           installed locales
//	set-keymap MAP [TOGGLEMAP]             console keymap
//	list-keymaps                           installed console keymaps
//	set-x11-keymap LAYOUT [MODEL [VARIANT [OPTIONS]]]
//	list-x11-keymap-models|layouts|options
//	list-x11-keymap-variants [LAYOUT]
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/locale"
)

var version = "dev"

// xkbRules is xkeyboard-config's list of models, layouts, variants and
// options.
var xkbRules = "/usr/share/X11/xkb/rules/base.lst"

type options struct {
	cmd       string
	args      []string
	noConvert bool
	help      bool
	version   bool
}

func main() {
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "localectl: %v\n", err)
		os.Exit(2)
	}
	if opts.help {
		printHelp(os.Stdout)
		return
	}
	if opts.version {
		fmt.Println(version)
		return
	}
	if err := dispatch(os.Stdout, opts); err != nil {
		fmt.Fprintf(os.Stderr, "localectl: %v\n", err)
		os.Exit(1)
	}
}

func parseArgs(argv []string) (options, error) {
	var o options
	var pos []string
	for _, a := range argv {
		switch {
		case a == "-h" || a == "--help":
			o.help = true
		case a == "--version":
			o.version = true
		case a == "--no-convert":
			o.noConvert = true
		case a == "--no-pager" || a == "--no-ask-password":
			// accepted, nothing to do here
		case a == "-H" || a == "--host" || strings.HasPrefix(a, "--host=") ||
			a == "-M" || a == "--machine" || strings.HasPrefix(a, "--machine="):
			return o, errors.New("--host / --machine are not supported by slinit-localectl")
		case strings.HasPrefix(a, "-") && a != "-":
			return o, fmt.Errorf("unknown flag %q (try --help)", a)
		default:
			pos = append(pos, a)
		}
	}
	o.cmd = "status"
	if len(pos) > 0 {
		o.cmd, o.args = pos[0], pos[1:]
	}
	return o, nil
}

func dispatch(out io.Writer, o options) error {
	warn := func(f string, a ...any) { fmt.Fprintf(os.Stderr, "localectl: "+f+"\n", a...) }
	switch o.cmd {
	case "status":
		return status(out)
	case "set-locale":
		if len(o.args) == 0 {
			return errors.New("set-locale requires at least one LOCALE")
		}
		merged, change, err := locale.PlanLocale(o.args)
		if err != nil || !change {
			return err
		}
		return locale.WriteLocale(merged)
	case "list-locales":
		ls := locale.InstalledLocales()
		sort.Strings(ls)
		for _, l := range ls {
			fmt.Fprintln(out, l)
		}
		return nil
	case "set-keymap":
		if len(o.args) < 1 || len(o.args) > 2 {
			return errors.New("set-keymap takes MAP [TOGGLEMAP]")
		}
		keymap, toggle := o.args[0], ""
		if len(o.args) == 2 {
			toggle = o.args[1]
		}
		for _, k := range []string{keymap, toggle} {
			if k != "" && !locale.KeymapExists(k) {
				return fmt.Errorf("keymap %s is not installed", k)
			}
		}
		p, err := locale.PlanVConsole(keymap, toggle, !o.noConvert)
		if err != nil || !p.Change {
			return err
		}
		if p.Note != "" {
			warn("%s", p.Note)
		}
		if err := p.Apply(warn); err != nil {
			return err
		}
		if l, err := exec.LookPath("loadkeys"); err == nil {
			_ = exec.Command(l, "-q", keymap).Run()
		}
		return nil
	case "list-keymaps":
		for _, k := range locale.ListKeymaps() {
			fmt.Fprintln(out, k)
		}
		return nil
	case "set-x11-keymap":
		if len(o.args) < 1 || len(o.args) > 4 {
			return errors.New("set-x11-keymap takes LAYOUT [MODEL [VARIANT [OPTIONS]]]")
		}
		f := append(append([]string{}, o.args...), "", "", "")
		in := locale.X11{Layout: f[0], Model: f[1], Variant: f[2], Options: f[3]}
		if !locale.SafeX11(in) {
			return errors.New("invalid X11 keyboard layout")
		}
		p, err := locale.PlanX11(in, !o.noConvert)
		if err != nil || !p.Change {
			return err
		}
		if p.Note != "" {
			warn("%s", p.Note)
		}
		return p.Apply(warn)
	case "list-x11-keymap-models":
		return listRules(out, "model", "")
	case "list-x11-keymap-layouts":
		return listRules(out, "layout", "")
	case "list-x11-keymap-options":
		return listRules(out, "option", "")
	case "list-x11-keymap-variants":
		layout := ""
		if len(o.args) > 0 {
			layout = o.args[0]
		}
		return listRules(out, "variant", layout)
	}
	return fmt.Errorf("unknown command %q (try --help)", o.cmd)
}

func status(out io.Writer) error {
	m, err := locale.ReadLocale()
	if err != nil {
		return err
	}
	env := locale.LocaleEnv(m)
	if len(env) == 0 {
		env = []string{"n/a"}
	}
	fmt.Fprintf(out, "System Locale: %s\n", env[0])
	for _, e := range env[1:] {
		fmt.Fprintf(out, "               %s\n", e)
	}
	vc, _, _ := locale.ReadVConsole()
	x := locale.CurrentX11()
	field := func(name, v string, always bool) {
		if v == "" && !always {
			return
		}
		if v == "" {
			v = "n/a"
		}
		fmt.Fprintf(out, "%13s: %s\n", name, v)
	}
	field("VC Keymap", vc.Keymap, true)
	field("VC Toggle Keymap", vc.Toggle, false)
	field("X11 Layout", x.Layout, true)
	field("X11 Model", x.Model, false)
	field("X11 Variant", x.Variant, false)
	field("X11 Options", x.Options, false)
	return nil
}

// listRules prints one section of base.lst: "! model", "! layout",
// "! variant" (lines "  NAME LAYOUT: description") or "! option".
func listRules(out io.Writer, section, layout string) error {
	f, err := os.Open(xkbRules)
	if err != nil {
		return fmt.Errorf("cannot read the X11 keyboard rules: %w", err)
	}
	defer f.Close()
	in := false
	seen := map[string]bool{}
	var names []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := sc.Text()
		if strings.HasPrefix(l, "!") {
			in = strings.TrimSpace(strings.TrimPrefix(l, "!")) == section
			continue
		}
		fs := strings.Fields(l)
		if !in || len(fs) == 0 {
			continue
		}
		name := fs[0]
		if section == "variant" {
			if len(fs) < 2 || (layout != "" && strings.TrimSuffix(fs[1], ":") != layout) {
				continue
			}
		}
		if section == "option" && !strings.Contains(name, ":") {
			continue // a group heading, not an option
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintln(out, n)
	}
	return sc.Err()
}

func printHelp(out io.Writer) {
	fmt.Fprint(out, `slinit-localectl [OPTIONS...] COMMAND ...

Query or change the system locale and keyboard settings.

Commands:
  status                   Show current locale settings
  set-locale LOCALE...     Set system locale
  list-locales             Show known locales
  set-keymap MAP [MAP]     Set console and X11 keyboard mappings
  list-keymaps             Show known virtual console keyboard mappings
  set-x11-keymap LAYOUT [MODEL [VARIANT [OPTIONS]]]
                           Set X11 and console keyboard mappings
  list-x11-keymap-models   Show known X11 keyboard mapping models
  list-x11-keymap-layouts  Show known X11 keyboard mapping layouts
  list-x11-keymap-variants [LAYOUT]
                           Show known X11 keyboard mapping variants
  list-x11-keymap-options  Show known X11 keyboard mapping options

Options:
  -h --help                Show this help
     --version             Show package version
     --no-convert          Don't convert keyboard mappings
     --no-pager            Accepted for compatibility
     --no-ask-password     Accepted for compatibility

The settings are written directly; slinit-localed(8) serves the same
changes on the system bus for desktops.
`)
}
