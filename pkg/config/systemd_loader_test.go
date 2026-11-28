package config

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func writeUnit(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const probeUnit = `[Unit]
Description=Probe
[Service]
ExecStart=/usr/bin/probe
User=probe
`

// The point of the feature: a name with no native description resolves
// to a .service unit and runs, with no file generated anywhere.
func TestDirLoaderResolvesSystemdUnit(t *testing.T) {
	root := t.TempDir()
	native := filepath.Join(root, "slinit.d")
	units := filepath.Join(root, "systemd")
	os.MkdirAll(native, 0o755)
	writeUnit(t, units, "probe.service", probeUnit)

	dl := NewDirLoader(service.NewServiceSet(&testConsumerLogger{}), []string{native})
	dl.SetSystemdDirs([]string{units})

	desc, path, err := dl.findAndParse("probe")
	if err != nil {
		t.Fatalf("probe should resolve to the unit: %v", err)
	}
	if filepath.Base(path) != "probe.service" {
		t.Errorf("resolved path = %q, want the unit file", path)
	}
	if desc.RunAs != "probe" {
		t.Errorf("run-as = %q, want probe", desc.RunAs)
	}
	if len(desc.Command) == 0 {
		t.Error("command is empty")
	}
}

// Operators migrating from systemctl type the extension out of habit.
func TestDirLoaderAcceptsExplicitServiceSuffix(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "systemd")
	writeUnit(t, units, "probe.service", probeUnit)

	dl := NewDirLoader(service.NewServiceSet(&testConsumerLogger{}), []string{filepath.Join(root, "slinit.d")})
	dl.SetSystemdDirs([]string{units})

	if _, _, err := dl.findAndParse("probe.service"); err != nil {
		t.Fatalf("probe.service should resolve: %v", err)
	}
}

// A native description must win. A distro shipping both must not have
// slinit silently prefer the file the admin did not write.
func TestNativeDescriptionBeatsSystemdUnit(t *testing.T) {
	root := t.TempDir()
	native := filepath.Join(root, "slinit.d")
	units := filepath.Join(root, "systemd")
	os.MkdirAll(native, 0o755)
	os.WriteFile(filepath.Join(native, "probe"), []byte("type = process\ncommand = /native/binary\n"), 0o644)
	writeUnit(t, units, "probe.service", probeUnit)

	dl := NewDirLoader(service.NewServiceSet(&testConsumerLogger{}), []string{native})
	dl.SetSystemdDirs([]string{units})

	desc, path, err := dl.findAndParse("probe")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) == "probe.service" {
		t.Fatal("the systemd unit shadowed the native description")
	}
	if desc.Command[0] != "/native/binary" {
		t.Errorf("command = %v, want the native one", desc.Command)
	}
}

// Earlier directories win, matching systemd's own precedence: an admin
// override in /etc must beat the packaged unit in /usr/lib.
func TestSystemdDirPrecedence(t *testing.T) {
	root := t.TempDir()
	etc := filepath.Join(root, "etc")
	usr := filepath.Join(root, "usr")
	writeUnit(t, etc, "probe.service", "[Service]\nExecStart=/etc/version\n")
	writeUnit(t, usr, "probe.service", "[Service]\nExecStart=/usr/version\n")

	dl := NewDirLoader(service.NewServiceSet(&testConsumerLogger{}), []string{filepath.Join(root, "slinit.d")})
	dl.SetSystemdDirs([]string{etc, usr})

	desc, _, err := dl.findAndParse("probe")
	if err != nil {
		t.Fatal(err)
	}
	if desc.Command[0] != "/etc/version" {
		t.Errorf("command = %v, want the /etc unit to win", desc.Command)
	}
}

// `systemctl mask` puts a symlink to /dev/null (or an empty file) in /etc.
// That failed IsRegular and was skipped, so the loader went on to the
// packaged unit in /usr/lib and ran what the admin had masked.
func TestMaskedSystemdUnitIsNotLoaded(t *testing.T) {
	for _, how := range []string{"devnull", "empty"} {
		t.Run(how, func(t *testing.T) {
			root := t.TempDir()
			etc := filepath.Join(root, "etc")
			usr := filepath.Join(root, "usr")
			writeUnit(t, usr, "probe.service", probeUnit)
			if how == "empty" {
				writeUnit(t, etc, "probe.service", "")
			} else {
				os.MkdirAll(etc, 0o755)
				if err := os.Symlink("/dev/null", filepath.Join(etc, "probe.service")); err != nil {
					t.Fatal(err)
				}
			}

			dl := NewDirLoader(service.NewServiceSet(&testConsumerLogger{}), []string{filepath.Join(root, "slinit.d")})
			dl.SetSystemdDirs([]string{etc, usr})

			_, path, err := dl.findAndParse("probe")
			if err == nil || !strings.Contains(err.Error(), "masked") {
				t.Fatalf("findAndParse(probe) = %q, %v; want a masked error", path, err)
			}
			if dl.descriptionExists("probe") {
				t.Error("descriptionExists(probe) = true for a masked unit")
			}
		})
	}
}

// Only .service. A .timer or .socket has semantics that land on other
// slinit facilities, and loading one as a service would start something
// that never does what was asked.
func TestOnlyServiceUnitsAreLoaded(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "systemd")
	writeUnit(t, units, "probe.timer", "[Timer]\nOnCalendar=daily\n")
	writeUnit(t, units, "probe.socket", "[Socket]\nListenStream=9000\n")

	dl := NewDirLoader(service.NewServiceSet(&testConsumerLogger{}), []string{filepath.Join(root, "slinit.d")})
	dl.SetSystemdDirs([]string{units})

	for _, n := range []string{"probe", "probe.timer", "probe.socket"} {
		if _, _, err := dl.findAndParse(n); err == nil {
			t.Errorf("%q should not resolve: only .service units load", n)
		}
	}
}

// Disabled by default: without SetSystemdDirs nothing is searched.
func TestSystemdFallbackOffByDefault(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "systemd")
	writeUnit(t, units, "probe.service", probeUnit)

	dl := NewDirLoader(service.NewServiceSet(&testConsumerLogger{}), []string{filepath.Join(root, "slinit.d")})
	if _, _, err := dl.findAndParse("probe"); err == nil {
		t.Error("units resolved without SetSystemdDirs being called")
	}
}

// Conversion notes have no terminal to reach when a unit is loaded
// live, so they go through the hook. A dropped hardening directive the
// operator never hears about is the failure this prevents.
func TestSystemdWarningsReachTheHook(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "systemd")
	writeUnit(t, units, "probe.service", `[Service]
ExecStart=/usr/bin/probe
PrivateNetwork=yes
`)
	var got []string
	OnSystemdUnitWarning = func(svc, path, level, msg string) {
		got = append(got, level+": "+msg)
	}
	t.Cleanup(func() { OnSystemdUnitWarning = nil })

	dl := NewDirLoader(service.NewServiceSet(&testConsumerLogger{}), []string{filepath.Join(root, "slinit.d")})
	dl.SetSystemdDirs([]string{units})
	if _, _, err := dl.findAndParse("probe"); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Error("PrivateNetwork has no slinit equivalent and must be reported, not dropped silently")
	}
}

// systemd's After= orders and Wants= pulls in only what exists: a unit
// naming one that is not installed still starts. slinit fails a load on
// a missing dependency, so 92 of 253 host units failed to load directly
// (display-manager.service: After=ypbind.service); a unit loaded
// directly now leaves such a reference out with a note. After= that
// does resolve is after:, ordering only, as in systemd (through 4.0 it
// was waits-for:, which also started the unit).
func TestSystemdOrderingOnMissingUnitsLoads(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "systemd")
	writeUnit(t, units, "dm.service", `[Unit]
After=ypbind.service peer.service
Wants=gone.service
[Service]
ExecStart=/usr/bin/dm
`)
	writeUnit(t, units, "peer.service", "[Service]\nExecStart=/usr/bin/peer\n")
	var got []string
	OnSystemdUnitWarning = func(svc, path, level, msg string) {
		got = append(got, level+": "+msg)
	}
	t.Cleanup(func() { OnSystemdUnitWarning = nil })

	ss := service.NewServiceSet(&testConsumerLogger{})
	dl := NewDirLoader(ss, []string{filepath.Join(root, "slinit.d")})
	dl.SetSystemdDirs([]string{units})
	ss.SetLoader(dl)
	svc, err := dl.LoadService("dm")
	if err != nil {
		t.Fatalf("unit with After=/Wants= on missing units does not load: %v", err)
	}
	deps := map[service.DependencyType]bool{}
	for _, d := range svc.Record().Dependencies() {
		if d.To.Name() != "peer" {
			t.Errorf("unexpected dependency on %q", d.To.Name())
		}
		deps[d.DepType] = true
	}
	if !deps[service.DepAfter] || len(deps) != 1 {
		t.Errorf("peer dependencies = %v, want after: only: After= orders, it does not start peer", deps)
	}
	for _, missing := range []string{"ypbind", "gone"} {
		found := false
		for _, g := range got {
			found = found || (strings.HasPrefix(g, "NOTE") && strings.Contains(g, missing))
		}
		if !found {
			t.Errorf("no NOTE that %s was left out: %v", missing, got)
		}
	}
}

// systemd quotes an Exec*= word the way slinit's command parser does.
// Stripping the "outer" quotes off the whole value, as is right for
// most keys, cut the first and last word's quotes instead: argv came
// out as ["/opt/my" "app/bin/run --name hello" "world"].
func TestSystemdExecQuotesKept(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "systemd")
	writeUnit(t, units, "q.service", `[Service]
ExecStart="/opt/my app/bin/run" --name "hello world"
ExecStop='/opt/my app/bin/stop' now
`)
	dl := NewDirLoader(service.NewServiceSet(&testConsumerLogger{}), []string{filepath.Join(root, "slinit.d")})
	dl.SetSystemdDirs([]string{units})
	desc, _, err := dl.findAndParse("q")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/opt/my app/bin/run", "--name", "hello world"}
	if strings.Join(desc.Command, "|") != strings.Join(want, "|") {
		t.Errorf("command = %q, want %q", desc.Command, want)
	}
	want = []string{"/opt/my app/bin/stop", "now"}
	if strings.Join(desc.StopCommand, "|") != strings.Join(want, "|") {
		t.Errorf("stop-command = %q, want %q", desc.StopCommand, want)
	}
}

// Only a .service or .socket reference names something slinit can
// depend on. The other unit types were copied through with the suffix
// cut off, or verbatim, so qemu-guest-agent.service (After= its virtio
// .device) could not load: the escaped device name is no valid service
// name. Like .target, they are left out with a note.
func TestSystemdNonServiceRefsDropped(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "systemd")
	writeUnit(t, units, "ga.service", `[Unit]
BindsTo=dev-virtio\x2dports-org.qemu.guest_agent.0.device
After=dev-virtio\x2dports-org.qemu.guest_agent.0.device
Requires=-.mount
Wants=user.slice
[Service]
ExecStart=/usr/bin/qemu-ga
`)
	var got []string
	OnSystemdUnitWarning = func(svc, path, level, msg string) {
		got = append(got, level+": "+msg)
	}
	t.Cleanup(func() { OnSystemdUnitWarning = nil })

	ss := service.NewServiceSet(&testConsumerLogger{})
	dl := NewDirLoader(ss, []string{filepath.Join(root, "slinit.d")})
	dl.SetSystemdDirs([]string{units})
	ss.SetLoader(dl)
	svc, err := dl.LoadService("ga")
	if err != nil {
		t.Fatalf("unit referring to non-service units does not load: %v", err)
	}
	if deps := svc.Record().Dependencies(); len(deps) != 0 {
		t.Errorf("dependencies on %v, want none", deps[0].To.Name())
	}
	for _, ref := range []string{"guest_agent.0.device", "-.mount", "user.slice"} {
		found := false
		for _, g := range got {
			found = found || (strings.HasPrefix(g, "NOTE") && strings.Contains(g, ref))
		}
		if !found {
			t.Errorf("no NOTE that %s was dropped: %v", ref, got)
		}
	}
}

// X.socket activates X.service, and slinit has no socket units, so a
// reference to the socket is a dependency on the service: on 4.0.2
// lvm2-monitor's Requires=dm-event.socket was depends-on: dm-event, and
// dropping every non-.service reference lost that hard dependency (and
// cups.socket, dbus.socket, syslog.socket elsewhere). A unit's own
// socket names the unit itself — dm-event.service Requires= its socket —
// and is dropped with a note, not made a dependency on itself. A
// Requires= on a socket whose service does not exist still fails the
// load, as a Requires= on a missing service does.
func TestSystemdSocketRefIsItsService(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "systemd")
	writeUnit(t, units, "lvm2-monitor.service", `[Unit]
Requires=dm-event.socket
After=dm-event.socket dm-event.service
[Service]
Type=oneshot
ExecStart=/usr/sbin/lvm vgchange --monitor y
`)
	writeUnit(t, units, "dm-event.service", `[Unit]
Requires=dm-event.socket
After=dm-event.socket
[Service]
ExecStart=/usr/sbin/dmeventd -f
`)
	writeUnit(t, units, "dbus-broker.service", `[Unit]
Requires=dbus.socket
After=dbus.socket
[Service]
ExecStart=/usr/bin/dbus-broker-launch
[Install]
Alias=dbus.service
`)
	writeUnit(t, units, "needs-gone.service", `[Unit]
Requires=gone.socket
[Service]
ExecStart=/bin/true
`)
	var got []string
	OnSystemdUnitWarning = func(svc, path, level, msg string) {
		got = append(got, svc+" "+level+": "+msg)
	}
	t.Cleanup(func() { OnSystemdUnitWarning = nil })

	ss := service.NewServiceSet(&testConsumerLogger{})
	dl := NewDirLoader(ss, []string{filepath.Join(root, "slinit.d")})
	dl.SetSystemdDirs([]string{units})
	ss.SetLoader(dl)
	svc, err := dl.LoadService("lvm2-monitor")
	if err != nil {
		t.Fatalf("load lvm2-monitor: %v", err)
	}
	hard := false
	for _, d := range svc.Record().Dependencies() {
		if d.To.Name() != "dm-event" {
			t.Errorf("unexpected dependency on %q", d.To.Name())
		}
		hard = hard || d.DepType == service.DepRegular
	}
	if !hard {
		t.Error("Requires=dm-event.socket did not become depends-on: dm-event")
	}
	ev := ss.FindService("dm-event", false)
	if ev == nil {
		t.Fatal("dm-event not loaded")
	}
	for _, d := range ev.Record().Dependencies() {
		t.Errorf("dm-event depends on %q (%d); its own socket is itself", d.To.Name(), d.DepType)
	}
	noted := false
	for _, g := range got {
		noted = noted || (strings.HasPrefix(g, "dm-event NOTE") && strings.Contains(g, "dm-event.socket") && strings.Contains(g, "itself"))
	}
	if !noted {
		t.Errorf("no NOTE that dm-event's own socket was dropped: %v", got)
	}
	// dbus.socket is dbus-broker's own, through Alias=dbus.service.
	br, err := dl.LoadService("dbus-broker")
	if err != nil {
		t.Fatalf("load dbus-broker: %v", err)
	}
	for _, d := range br.Record().Dependencies() {
		t.Errorf("dbus-broker depends on %q; dbus is its own alias", d.To.Name())
	}
	if _, err := dl.LoadService("needs-gone"); err == nil {
		t.Error("Requires=gone.socket with no gone service loaded; a missing Requires= must fail")
	}
}

// User=, Group=, Restart=on-success and several ExecStart= lines, each
// read through the real loader.
//   - Group= before User= made run-as "user::group", a group named
//     ":group"; systemd does not care about the order.
//   - Restart=on-success became restart = yes, so a service that failed
//     was restarted; slinit has had on-success since 3.x. (On a simple
//     service: systemd refuses it on a oneshot, and so does slinit.)
//   - A oneshot runs every ExecStart= in turn; slinit runs one command,
//     and the others were dropped without a word. Since 4.1 they run in
//     turn from one shell, with a note.
func TestSystemdUserGroupRestartExecStart(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "systemd")
	writeUnit(t, units, "o.service", `[Service]
Type=oneshot
Group=wheel
User=nobody
ExecStart=/usr/bin/first
ExecStart=/usr/bin/second
`)
	var got []string
	OnSystemdUnitWarning = func(svc, path, level, msg string) {
		got = append(got, level+": "+msg)
	}
	t.Cleanup(func() { OnSystemdUnitWarning = nil })
	dl := NewDirLoader(service.NewServiceSet(&testConsumerLogger{}), []string{filepath.Join(root, "slinit.d")})
	dl.SetSystemdDirs([]string{units})
	desc, _, err := dl.findAndParse("o")
	if err != nil {
		t.Fatal(err)
	}
	if desc.RunAs != "nobody:wheel" {
		t.Errorf("run-as = %q, want nobody:wheel", desc.RunAs)
	}
	writeUnit(t, units, "s.service", "[Service]\nRestart=on-success\nExecStart=/usr/bin/s\n")
	if sd, _, err := dl.findAndParse("s"); err != nil || sd.AutoRestart != service.RestartOnSuccess {
		t.Errorf("Restart=on-success: restart = %v (err %v), want on-success", sd.AutoRestart, err)
	}
	if want := []string{"/bin/sh", "-c", "'/usr/bin/first' && '/usr/bin/second'"}; strings.Join(desc.Command, "|") != strings.Join(want, "|") {
		t.Errorf("command %q, want %q", desc.Command, want)
	}
	found := false
	for _, g := range got {
		found = found || (strings.HasPrefix(g, "NOTE") && strings.Contains(g, "2 ExecStart= lines run in turn"))
	}
	if !found {
		t.Errorf("no NOTE that the ExecStart= lines run in turn: %v", got)
	}
}

// What systemd substitutes into an Exec line and slinit cannot:
//   - $VAR comes from Environment=/EnvironmentFile= in systemd; slinit
//     expands it when the file is loaded, from its own environment, so
//     ${FILTER_RPC_ARGS} from qemu-ga's env-file became nothing. Warned.
//   - $MAINPID: slinit has none, so `kill -s SIG $MAINPID` lost its PID.
//     That form is the term-signal when SIG stops the daemon (TERM, INT,
//     QUIT, KILL, or KillSignal='s own); a HUP or USR1 is a reload, and
//     as term-signal it overrode KillSignal=, leaving the daemon running
//     until stop-timeout and SIGKILL — dropped with a warning instead.
//     Any other use drops the stop-command.
//   - A second EnvironmentFile= replaced the first silently; slinit
//     reads one. Warned.
func TestSystemdExecSubstitutions(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "systemd")
	writeUnit(t, units, "v.service", `[Service]
EnvironmentFile=-/etc/a
EnvironmentFile=-/etc/b
ExecStart=/usr/bin/d $OPTS --x ${MORE} $$literal
ExecStop=/bin/kill -s HUP $MAINPID
KillSignal=INT
`)
	writeUnit(t, units, "q.service", `[Service]
ExecStart=/usr/bin/q
ExecStop=/bin/kill -QUIT $MAINPID
`)
	writeUnit(t, units, "u.service", `[Service]
ExecStart=/usr/bin/u
ExecStop=/bin/kill -s USR1 ${MAINPID}
`)
	writeUnit(t, units, "k.service", `[Service]
ExecStart=/usr/bin/k
KillSignal=SIGUSR2
ExecStop=/bin/kill -USR2 $MAINPID
`)
	writeUnit(t, units, "b.service", `[Service]
ExecStart=/usr/bin/b
ExecStop=/bin/kill -s QUIT ${MAINPID}
`)
	writeUnit(t, units, "w.service", `[Service]
ExecStart=/usr/bin/w
ExecStop=/usr/bin/wctl stop ${MAINPID}
`)
	var got []string
	OnSystemdUnitWarning = func(svc, path, level, msg string) {
		got = append(got, level+": "+svc+": "+msg)
	}
	t.Cleanup(func() { OnSystemdUnitWarning = nil })
	dl := NewDirLoader(service.NewServiceSet(&testConsumerLogger{}), []string{filepath.Join(root, "slinit.d")})
	dl.SetSystemdDirs([]string{units})
	v, _, err := dl.findAndParse("v")
	if err != nil {
		t.Fatal(err)
	}
	if v.TermSignal != syscall.SIGINT || len(v.StopCommand) != 0 {
		t.Errorf("term-signal = %v, stop-command = %q; want KillSignal's SIGINT and none: HUP does not stop", v.TermSignal, v.StopCommand)
	}
	for name, want := range map[string]syscall.Signal{
		"q": syscall.SIGQUIT,
		"u": syscall.SIGTERM, // the default: USR1 is no stop signal
		"k": syscall.SIGUSR2, // KillSignal= names it, so it is the stop signal
		"b": syscall.SIGQUIT, // ${MAINPID} as well as $MAINPID
	} {
		d, _, err := dl.findAndParse(name)
		if err != nil {
			t.Fatal(err)
		}
		if d.TermSignal != want || len(d.StopCommand) != 0 {
			t.Errorf("%s: term-signal = %v, stop-command = %q; want %v and none", name, d.TermSignal, d.StopCommand, want)
		}
	}
	w, _, err := dl.findAndParse("w")
	if err != nil {
		t.Fatal(err)
	}
	if len(w.StopCommand) != 0 {
		t.Errorf("stop-command = %q, want it dropped: it has no PID to act on", w.StopCommand)
	}
	for _, want := range []string{"/etc/a", "w: ExecStop", "v: ExecStop", "u: ExecStop"} {
		found := false
		for _, g := range got {
			found = found || (strings.HasPrefix(g, "WARN") && strings.Contains(g, want))
		}
		if !found {
			t.Errorf("no WARN mentioning %q: %v", want, got)
		}
	}
	// Since 4.1 ExecStart='s variables are expanded when it runs, from
	// the service's environment: a note, not a warning.
	found := false
	for _, g := range got {
		found = found || strings.HasPrefix(g, "NOTE: v: ExecStart= uses $OPTS ${MORE}")
	}
	if !found {
		t.Errorf("no NOTE that ExecStart='s $OPTS ${MORE} are expanded at run time: %v", got)
	}
	for _, g := range got {
		if strings.Contains(g, "literal") {
			t.Errorf("$$ is a literal $ on both sides, not a variable: %s", g)
		}
	}
}
