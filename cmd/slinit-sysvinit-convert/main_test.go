package main

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/config"
)

// The one test that matters most: everything this converter emits has to
// be something slinit will actually load. A converter whose output the
// real parser rejects is worse than no converter — the operator finds out
// at boot. So the emitted text goes through config.Parse, the same
// function the daemon uses, rather than through a check written here.
func TestEveryEmittedServiceParses(t *testing.T) {
	const inittab = `# a classic inittab
id:3:initdefault:
si::sysinit:/etc/init.d/rcS
1:2345:respawn:/sbin/agetty --noclear 38400 tty1 linux
S0:3:respawn:/sbin/agetty -L 115200 ttyS0 vt100
ca::ctrlaltdel:/sbin/shutdown -t3 -r now
pf::powerwait:/etc/init.d/powerfail start
x:5:off:/usr/bin/xdm -nodaemon
rc:2345:wait:/etc/init.d/rc 3
on:a:ondemand:/usr/sbin/ondemand-thing
bw::bootwait:/etc/init.d/early
`
	entries, errs := parseInittab(strings.NewReader(inittab))
	if len(errs) != 0 {
		t.Fatalf("parse errors on a valid inittab: %v", errs)
	}
	res := convert(entries, dialectSysv)
	if len(res.services) == 0 {
		t.Fatal("no services produced")
	}

	for name, body := range res.services {
		desc, err := config.Parse(strings.NewReader(body), name, name)
		if err != nil {
			t.Errorf("slinit rejects the file generated for %q: %v\n--- file ---\n%s", name, err, body)
			continue
		}
		if len(desc.Command) == 0 {
			t.Errorf("%q parsed but has no command", name)
		}
	}
}

func TestParseInittabGrammar(t *testing.T) {
	// The process field may contain colons, so it must never be split
	// further — a shell command is a perfectly normal thing to find there.
	in := `x:2:respawn:/bin/sh -c "echo a:b:c"`
	entries, errs := parseInittab(strings.NewReader(in))
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if got := entries[0].process; got != `/bin/sh -c "echo a:b:c"` {
		t.Errorf("process = %q — the colons in the command were split", got)
	}
}

func TestParseInittabRejectsMalformed(t *testing.T) {
	// Three fields is not an inittab line. Reported with its line number
	// rather than skipped silently, because a dropped entry is a service
	// that quietly stops existing.
	_, errs := parseInittab(strings.NewReader("a:b:c\n"))
	if len(errs) != 1 {
		t.Fatalf("got %d errors, want 1: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0], "line 1") {
		t.Errorf("error does not name the line: %q", errs[0])
	}
}

// An action neither dialect defines is refused by name, never guessed:
// a service that silently does the wrong thing is the worst outcome
// available mid-migration.
//
// `askfirst` was this test's example until busybox support landed and
// made it a known action, so the example is now something genuinely
// absent from both tables.
func TestUnknownActionIsRefusedByName(t *testing.T) {
	entries, _ := parseInittab(strings.NewReader("tty1::sometimes:/bin/sh\n"))
	res := convert(entries, dialectSysv)
	if len(res.services) != 0 {
		t.Errorf("an unknown action produced a service file: %v", res.services)
	}
	if len(res.warnings) != 1 {
		t.Fatalf("got %d warnings, want 1: %v", len(res.warnings), res.warnings)
	}
	if !strings.Contains(res.warnings[0], "sometimes") {
		t.Errorf("the warning does not name the action: %q", res.warnings[0])
	}
}

func TestActionMapping(t *testing.T) {
	cases := []struct {
		action   string
		wantType string
		wantHas  []string
		wantNot  []string
	}{
		{"respawn", "type = process", []string{"restart = yes"}, []string{"manual = yes"}},
		{"wait", "type = scripted", nil, []string{"restart = yes"}},
		{"once", "type = scripted", nil, []string{"restart = yes"}},
		{"boot", "type = scripted", nil, []string{"restart = yes"}},
		{"bootwait", "type = scripted", nil, []string{"restart = yes"}},
		// `off` means disabled: kept for reference, never started.
		{"off", "type = process", []string{"manual = yes"}, nil},
		// `ondemand` is triggered, not booted.
		{"ondemand", "type = process", []string{"manual = yes"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			entries, _ := parseInittab(strings.NewReader("zz:2:" + tc.action + ":/bin/true\n"))
			res := convert(entries, dialectSysv)
			if len(res.services) != 1 {
				t.Fatalf("got %d services, want 1", len(res.services))
			}
			var body string
			for _, b := range res.services {
				body = b
			}
			if !strings.Contains(body, tc.wantType) {
				t.Errorf("missing %q:\n%s", tc.wantType, body)
			}
			for _, w := range tc.wantHas {
				if !strings.Contains(body, w) {
					t.Errorf("missing %q:\n%s", w, body)
				}
			}
			for _, w := range tc.wantNot {
				if strings.Contains(body, w) {
					t.Errorf("unexpected %q:\n%s", w, body)
				}
			}
		})
	}
}

// The powerfail family maps onto a feature that now exists, rather than
// being reported as unsupported: SIGPWR is dispatched to one hook with
// the state as its argument, so four inittab actions become four cases.
func TestPowerActionsPointAtThePowerHook(t *testing.T) {
	want := map[string]string{
		"powerwait":    "`failing`",
		"powerfail":    "`failing`",
		"powerokwait":  "`ok`",
		"powerfailnow": "`low`",
	}
	for action, arg := range want {
		entries, _ := parseInittab(strings.NewReader("pf::" + action + ":/etc/init.d/pf\n"))
		res := convert(entries, dialectSysv)
		if len(res.services) != 0 {
			t.Errorf("%s produced a service file; it is a hook case, not a service", action)
		}
		if len(res.notes) != 1 {
			t.Fatalf("%s: got %d notes, want 1", action, len(res.notes))
		}
		if !strings.Contains(res.notes[0], "power-hook") || !strings.Contains(res.notes[0], arg) {
			t.Errorf("%s note does not point at the hook with %s: %q", action, arg, res.notes[0])
		}
	}
}

// A getty keeps its utmp identity: the inittab id column is exactly what
// sysvinit writes into utmp, and slinit has directives for it, so who(1)
// and last(1) keep working after a conversion. The filename is lowercased
// but the values must not be.
func TestGettyKeepsItsUtmpIdentity(t *testing.T) {
	entries, _ := parseInittab(strings.NewReader("S0:3:respawn:/sbin/agetty -L 115200 ttyS0 vt100\n"))
	res := convert(entries, dialectSysv)
	body, ok := res.services["getty-ttys0"]
	if !ok {
		t.Fatalf("expected a service named getty-ttys0, got %v", keys(res.services))
	}
	for _, want := range []string{"inittab-id = S0", "inittab-line = ttyS0"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q — the tty's case was mangled:\n%s", want, body)
		}
	}
}

// Two entries that sanitise to one name must not overwrite each other.
// Silently losing a service is worse than refusing to convert it.
func TestNameClashIsRefusedNotOverwritten(t *testing.T) {
	// Same command, two ids that sanitise to the same thing — which is
	// what actually collides, since the name is built from both. (A first
	// version of this test used different commands and so never clashed:
	// the test was wrong, not the converter.)
	in := "a-b:2:respawn:/bin/true\na.b:2:respawn:/bin/true\n"
	entries, _ := parseInittab(strings.NewReader(in))
	res := convert(entries, dialectSysv)
	if len(res.services) != 1 {
		t.Fatalf("got %d services, want 1", len(res.services))
	}
	if len(res.warnings) != 1 || !strings.Contains(res.warnings[0], "already used") {
		t.Errorf("the clash was not reported: %v", res.warnings)
	}
}

// Runlevels 0 and 6 are halt and reboot. A service "in" them runs while
// the system goes down, which slinit expresses with stop-command rather
// than membership, so they must not become enable commands.
func TestHaltAndRebootLevelsAreNotWired(t *testing.T) {
	entries, _ := parseInittab(strings.NewReader("zz:016:respawn:/bin/true\n"))
	res := convert(entries, dialectSysv)
	for _, c := range res.enableCmds {
		if strings.Contains(c, "runlevel-0") || strings.Contains(c, "runlevel-6") {
			t.Errorf("wired a shutdown runlevel: %q", c)
		}
	}
	if len(res.enableCmds) != 1 || !strings.Contains(res.enableCmds[0], "runlevel-1") {
		t.Errorf("enable commands = %v, want only runlevel-1", res.enableCmds)
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The first column is the one place the dialects genuinely disagree:
// sysvinit writes it to utmp as an id, busybox uses it as the tty to run
// the command on. Both readings come from the respective sources.
func TestFirstColumnMeansDifferentThingsPerDialect(t *testing.T) {
	const line = "tty2::respawn:/sbin/getty 38400 tty2\n"

	entries, _ := parseInittab(strings.NewReader(line))
	for _, body := range convert(entries, dialectSysv).services {
		if !strings.Contains(body, "inittab-id = tty2") {
			t.Errorf("sysvinit: first column should become inittab-id:\n%s", body)
		}
		if strings.Contains(body, "tty-path") {
			t.Errorf("sysvinit: must not emit tty-path:\n%s", body)
		}
	}

	entries, _ = parseInittab(strings.NewReader(line))
	for _, body := range convert(entries, dialectBusybox).services {
		if !strings.Contains(body, "tty-path = /dev/tty2") {
			t.Errorf("busybox: first column should become tty-path:\n%s", body)
		}
		if strings.Contains(body, "inittab-id") {
			t.Errorf("busybox: must not emit inittab-id:\n%s", body)
		}
	}
}

// Detection, from the evidence each dialect leaves behind. A unique
// action settles it; failing that, busybox's shape is an unread runlevel
// column plus a tty in the first one.
func TestDetectDialect(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want dialect
	}{
		{"busybox-only action", "tty1::askfirst:/bin/sh\n", dialectBusybox},
		{"busybox shutdown", "::shutdown:/bin/umount -a\n", dialectBusybox},
		{"sysv-only action", "id:3:initdefault:\n", dialectSysv},
		{"sysv powerfail", "pf::powerwait:/etc/init.d/pf\n", dialectSysv},
		{"no runlevels, tty ids", "tty1::respawn:/sbin/getty tty1\n::sysinit:/etc/rcS\n", dialectBusybox},
		{"runlevels present", "1:2345:respawn:/sbin/agetty tty1\n", dialectSysv},
		{"non-tty id", "si::sysinit:/etc/init.d/rcS\n", dialectSysv},
		{"nothing to go on", "# just a comment\n", dialectSysv},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries, _ := parseInittab(strings.NewReader(tc.in))
			if got := detectDialect(entries); got != tc.want {
				t.Errorf("detectDialect = %q, want %q", got, tc.want)
			}
		})
	}
}

// busybox never reads the runlevel column, so turning it into runlevel
// membership would invent behaviour the original file never had.
func TestBusyboxRunlevelsAreNotWired(t *testing.T) {
	entries, _ := parseInittab(strings.NewReader("tty1:2345:respawn:/sbin/getty tty1\n"))
	res := convert(entries, dialectBusybox)
	if len(res.enableCmds) != 0 {
		t.Errorf("busybox runlevels were wired: %v", res.enableCmds)
	}
	if len(res.notes) == 0 {
		t.Error("the ignored runlevel column was not reported")
	}
}

// busybox strips a leading dash and gives the command a controlling tty.
// tty-path already arranges that, so the dash has to come off the
// command — left in, it would be exec'd as part of the path.
func TestBusyboxLeadingDashIsStripped(t *testing.T) {
	entries, _ := parseInittab(strings.NewReader("tty1::respawn:-/bin/sh\n"))
	res := convert(entries, dialectBusybox)
	if len(res.services) != 1 {
		t.Fatalf("got %d services, want 1", len(res.services))
	}
	for _, body := range res.services {
		if !strings.Contains(body, "command = /bin/sh") {
			t.Errorf("the leading dash survived into the command:\n%s", body)
		}
	}
	if !strings.Contains(strings.Join(res.notes, " "), "controlling tty") {
		t.Errorf("dropping the dash was not explained: %v", res.notes)
	}
}

// askfirst becomes a plain respawn and says so: slinit has no "wait for
// Enter" equivalent, and a console meant to stay quiet would otherwise
// come up without anyone noticing.
func TestAskfirstIsConvertedAndFlagged(t *testing.T) {
	entries, _ := parseInittab(strings.NewReader("tty3::askfirst:/bin/sh\n"))
	res := convert(entries, dialectBusybox)
	if len(res.services) != 1 {
		t.Fatalf("got %d services, want 1", len(res.services))
	}
	for _, body := range res.services {
		if !strings.Contains(body, "restart = yes") {
			t.Errorf("askfirst should respawn:\n%s", body)
		}
	}
	if !strings.Contains(strings.Join(res.notes, " "), "askfirst") {
		t.Errorf("the lost prompt was not reported: %v", res.notes)
	}
}

// busybox's shutdown and restart are not services at all.
func TestBusyboxShutdownAndRestartAreReported(t *testing.T) {
	in := "::shutdown:/bin/umount -a\n::restart:/sbin/init\n"
	entries, _ := parseInittab(strings.NewReader(in))
	res := convert(entries, dialectBusybox)
	if len(res.services) != 0 {
		t.Errorf("shutdown/restart produced service files: %v", keys(res.services))
	}
	joined := strings.Join(res.notes, " ")
	if !strings.Contains(joined, "shutdown-hook") {
		t.Errorf("shutdown was not pointed at the shutdown hook: %v", res.notes)
	}
	if !strings.Contains(joined, "soft-reboot") {
		t.Errorf("restart was not pointed at soft-reboot: %v", res.notes)
	}
}
