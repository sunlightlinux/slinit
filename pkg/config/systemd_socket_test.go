package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func sockConvert(t *testing.T, dir, svcUnit string) (*ServiceDescription, []SystemdWarning) {
	t.Helper()
	p := writeUnit(t, dir, "foo.service", svcUnit)
	_, warns, err := ConvertSystemdUnitIn(p, []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	desc, err := SystemdUnitToServiceDescription(p, "foo", dir)
	if err != nil {
		t.Fatal(err)
	}
	return desc, warns
}

func sockWarned(warns []SystemdWarning, sub string) bool {
	for _, w := range warns {
		if strings.Contains(w.Msg, sub) {
			return true
		}
	}
	return false
}

const sockSvc = "[Service]\nExecStart=/usr/bin/foo\n"

func TestSystemdSocketPairIsRead(t *testing.T) {
	dir := t.TempDir()
	writeUnit(t, dir, "foo.socket", "[Socket]\nListenStream=/run/foo/foo.sock\nListenStream=8080\n"+
		"ListenStream=127.0.0.1:9090\nListenDatagram=[::1]:53\nSocketMode=0660\nSocketUser=0\nSocketGroup=0\n"+
		"Backlog=128\nFileDescriptorName=foo\n[Install]\nWantedBy=sockets.target\n")
	desc, warns := sockConvert(t, dir, sockSvc)
	want := []string{"/run/foo/foo.sock", "tcp:[::]:8080", "tcp:127.0.0.1:9090", "udp:[::1]:53"}
	if !slices.Equal(desc.SocketPaths, want) {
		t.Errorf("socket-listen %q, want %q", desc.SocketPaths, want)
	}
	if desc.SocketPerms != 0o660 || desc.SocketUID != 0 || desc.SocketGID != 0 {
		t.Errorf("perms %o uid %d gid %d", desc.SocketPerms, desc.SocketUID, desc.SocketGID)
	}
	if !sockWarned(warns, "FileDescriptorName=foo not carried") || sockWarned(warns, "Backlog") {
		t.Errorf("notes: %v", warns)
	}
}

func TestSystemdSocketEdges(t *testing.T) {
	for _, tc := range []struct {
		name, socket, svc string
		dropin            string
		listens           []string
		warn              string
	}{
		{name: "no socket unit", svc: sockSvc},
		{name: "a reset", socket: "[Socket]\nListenStream=/run/a\nListenStream=\nListenStream=/run/b\n", svc: sockSvc,
			listens: []string{"/run/b"}},
		{name: "drop-in adds", socket: "[Socket]\nListenStream=/run/a\n", dropin: "[Socket]\nListenStream=/run/b\n", svc: sockSvc,
			listens: []string{"/run/a", "/run/b"}},
		{name: "unix datagram", socket: "[Socket]\nListenDatagram=/run/d\nListenStream=/run/s\n", svc: sockSvc,
			listens: []string{"/run/s"}, warn: "no Unix datagram"},
		{name: "abstract", socket: "[Socket]\nListenStream=@foo\n", svc: sockSvc, warn: "abstract"},
		{name: "fifo", socket: "[Socket]\nListenFIFO=/run/f\n", svc: sockSvc, warn: "only stream and datagram"},
		{name: "accept", socket: "[Socket]\nListenStream=22\nAccept=yes\n", svc: sockSvc, warn: "Accept=yes"},
		{name: "for another service", socket: "[Socket]\nListenStream=/run/a\nService=bar.service\n", svc: sockSvc,
			warn: "is for bar.service"},
		{name: "Sockets= names another", svc: "[Service]\nExecStart=/usr/bin/foo\nSockets=other.socket\n",
			listens: []string{"/run/other"}},
		{name: "Sockets= missing", svc: "[Service]\nExecStart=/usr/bin/foo\nSockets=gone.socket\n", warn: "no such socket unit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.socket != "" {
				writeUnit(t, dir, "foo.socket", tc.socket)
			}
			writeUnit(t, dir, "other.socket", "[Socket]\nListenStream=/run/other\n")
			if tc.dropin != "" {
				os.MkdirAll(filepath.Join(dir, "foo.socket.d"), 0o755)
				os.WriteFile(filepath.Join(dir, "foo.socket.d", "x.conf"), []byte(tc.dropin), 0o644)
			}
			desc, warns := sockConvert(t, dir, tc.svc)
			if !slices.Equal(desc.SocketPaths, tc.listens) {
				t.Errorf("socket-listen %q, want %q", desc.SocketPaths, tc.listens)
			}
			if tc.warn != "" && !sockWarned(warns, tc.warn) {
				t.Errorf("no warning containing %q in %v", tc.warn, warns)
			}
		})
	}
}

// foo.socket with Accept=yes and the template foo@.service, no
// foo.service: loading foo is systemd's per-connection service, the
// template's command run once per connection (socket-accept).
func TestSystemdAcceptSocketWithTemplate(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "units")
	writeUnit(t, units, "lpd.socket", "[Socket]\nListenStream=515\nAccept=yes\n")
	writeUnit(t, units, "lpd@.service", "[Unit]\nRequires=lpd.socket\n[Service]\nExecStart=-/usr/lib/cups/daemon/cups-lpd\nStandardInput=socket\n")
	_, dl, _ := cycleLoader(t, root)
	desc, path, err := dl.findAndParse("lpd")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "lpd@.service" || !desc.SocketAccept || !slices.Equal(desc.SocketPaths, []string{"tcp:[::]:515"}) {
		t.Errorf("path %s accept %v listen %q", path, desc.SocketAccept, desc.SocketPaths)
	}
	if !dl.descriptionExists("lpd") {
		t.Error("descriptionExists does not see the accept pair")
	}

	// Accept=no with only a template: nothing to load.
	writeUnit(t, units, "x.socket", "[Socket]\nListenStream=516\n")
	writeUnit(t, units, "x@.service", "[Service]\nExecStart=/bin/x\n")
	if _, _, err := dl.findAndParse("x"); err == nil {
		t.Error("a template behind an Accept=no socket loaded as x")
	}
	// The converter reads it the same way.
	cfg, warns, err := ConvertSystemdAcceptUnitIn(filepath.Join(units, "lpd@.service"), "lpd", []string{units})
	if err != nil || cfg.svcName != "lpd" {
		t.Fatalf("%v %v", cfg, err)
	}
	if sockWarned(warns, "StandardInput") {
		t.Errorf("StandardInput=socket noted as a gap under socket-accept: %v", warns)
	}
}

// No SocketMode=: systemd's 0666, not slinit's 0600, or clients that
// are not root are turned away.
func TestSystemdSocketDefaultMode(t *testing.T) {
	dir := t.TempDir()
	writeUnit(t, dir, "foo.socket", "[Socket]\nListenStream=/run/foo.sock\n")
	desc, _ := sockConvert(t, dir, sockSvc)
	if desc.SocketPerms != 0o666 {
		t.Errorf("socket-permissions %o, want 0666", desc.SocketPerms)
	}
}

// A template behind an Accept=yes socket with no listener slinit can
// open is not loaded as a service with no socket.
func TestSystemdAcceptWithoutListenerIsNotLoaded(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "units")
	writeUnit(t, units, "cd.socket", "[Socket]\nListenSequentialPacket=/run/cd\nAccept=yes\n")
	writeUnit(t, units, "cd@.service", "[Service]\nExecStart=/bin/cd\n")
	_, dl, _ := cycleLoader(t, root)
	if _, _, err := dl.findAndParse("cd"); err == nil || !strings.Contains(err.Error(), "no listener") {
		t.Errorf("err %v, want refused for having no listener", err)
	}
}

// The pair loads by either spelling of the name, honours a mask in a
// higher directory (a symlink to /dev/null, systemctl mask's form) on
// either unit, and an Accept=yes set in a drop-in.
func TestSystemdAcceptPairEdges(t *testing.T) {
	setup := func(t *testing.T) (string, string, string) {
		root := t.TempDir()
		etc, lib := filepath.Join(root, "etc"), filepath.Join(root, "lib")
		writeUnit(t, lib, "lpd.socket", "[Socket]\nListenStream=515\nAccept=yes\n")
		writeUnit(t, lib, "lpd@.service", "[Service]\nExecStart=/bin/lpd\n")
		os.MkdirAll(etc, 0o755)
		return root, etc, lib
	}
	loader := func(etc, lib string) *DirLoader {
		ss := service.NewServiceSet(&testConsumerLogger{})
		dl := NewDirLoader(ss, []string{t.TempDir()})
		dl.SetSystemdDirs([]string{etc, lib})
		return dl
	}
	_, etc, lib := setup(t)
	if d, _, err := loader(etc, lib).findAndParse("lpd.service"); err != nil || !d.SocketAccept {
		t.Errorf("lpd.service: %v", err)
	}
	for _, masked := range []string{"lpd@.service", "lpd.socket"} {
		_, etc, lib := setup(t)
		os.Symlink("/dev/null", filepath.Join(etc, masked))
		dl := loader(etc, lib)
		if _, _, err := dl.findAndParse("lpd"); err == nil {
			t.Errorf("%s masked, yet lpd loaded", masked)
		}
		if dl.descriptionExists("lpd") {
			t.Errorf("%s masked, yet descriptionExists", masked)
		}
	}
	root := t.TempDir()
	units := filepath.Join(root, "u")
	writeUnit(t, units, "lpd.socket", "[Socket]\nListenStream=515\n")
	writeUnit(t, units, "lpd@.service", "[Service]\nExecStart=/bin/lpd\n")
	os.MkdirAll(filepath.Join(units, "lpd.socket.d"), 0o755)
	os.WriteFile(filepath.Join(units, "lpd.socket.d", "a.conf"), []byte("[Socket]\nAccept=yes\n"), 0o644)
	if d, _, err := loader(units, units).findAndParse("lpd"); err != nil || !d.SocketAccept {
		t.Errorf("Accept=yes from a drop-in: %v", err)
	}
}

// reload-command needs a process to reload.
func TestReloadCommandTypes(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "slinit.d")
	os.MkdirAll(dir, 0o755)
	writeServiceFile(t, dir, "s", "type = scripted\ncommand = /bin/true\nreload-command = /bin/true\n")
	writeServiceFile(t, dir, "b", "type = bgprocess\ncommand = /bin/true\npid-file = /run/b.pid\nreload-command = /bin/true\n")
	_, dl, _ := cycleLoader(t, root)
	if _, err := dl.LoadService("s"); err == nil || !strings.Contains(err.Error(), "reload-command") {
		t.Errorf("scripted with reload-command: %v, want refused", err)
	}
	if _, err := dl.LoadService("b"); err != nil {
		t.Errorf("bgprocess with reload-command: %v", err)
	}
	p := writeUnit(t, t.TempDir(), "o.service", "[Service]\nType=oneshot\nExecStart=/bin/true\nExecReload=/bin/r\n")
	cfg, warns, err := ConvertSystemdUnit(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.hasMapped("reload-command"); ok || !sockWarned(warns, "reload-command for a process or bgprocess") {
		t.Errorf("oneshot ExecReload=: %v", warns)
	}
}

func TestSystemdTimerRead(t *testing.T) {
	for _, tc := range []struct {
		name, timer, dropin string
		cal                 string
		manual              bool
		warn                string
	}{
		{name: "daily", timer: "[Timer]\nOnCalendar=daily\nAccuracySec=1h\n", cal: "daily", manual: true, warn: "AccuracySec=1h not carried"},
		{name: "drop-in resets and sets", timer: "[Timer]\nOnCalendar=daily\n", dropin: "[Timer]\nOnCalendar=\nOnCalendar=Mon 04:00\n", cal: "Mon 04:00", manual: true},
		{name: "two expressions", timer: "[Timer]\nOnCalendar=daily\nOnCalendar=weekly\n", cal: "daily", manual: true, warn: "takes one expression"},
		{name: "monotonic unsupported", timer: "[Timer]\nOnActiveSec=5min\n", warn: "count from the boot"},
		{name: "for another unit", timer: "[Timer]\nOnCalendar=daily\nUnit=other.service\n", warn: "not this service"},
		{name: "bad expression", timer: "[Timer]\nOnCalendar=Funday\n", warn: "no calendar expression"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeUnit(t, dir, "foo.timer", tc.timer)
			if tc.dropin != "" {
				os.MkdirAll(filepath.Join(dir, "foo.timer.d"), 0o755)
				os.WriteFile(filepath.Join(dir, "foo.timer.d", "x.conf"), []byte(tc.dropin), 0o644)
			}
			desc, warns := sockConvert(t, dir, sockSvc)
			if desc.StartOnCalendar != tc.cal || desc.ManualStart != tc.manual {
				t.Errorf("start-on-calendar %q manual %v, want %q %v", desc.StartOnCalendar, desc.ManualStart, tc.cal, tc.manual)
			}
			if tc.warn != "" && !sockWarned(warns, tc.warn) {
				t.Errorf("no warning containing %q: %v", tc.warn, warns)
			}
		})
	}
	// A masked timer is not read.
	root := t.TempDir()
	etc, lib := filepath.Join(root, "etc"), filepath.Join(root, "lib")
	writeUnit(t, lib, "foo.timer", "[Timer]\nOnCalendar=daily\n")
	os.MkdirAll(etc, 0o755)
	os.Symlink("/dev/null", filepath.Join(etc, "foo.timer"))
	p := writeUnit(t, lib, "foo.service", sockSvc)
	d, err := SystemdUnitToServiceDescription(p, "foo", etc, lib)
	if err != nil || d.StartOnCalendar != "" {
		t.Errorf("masked timer read: %q %v", d.StartOnCalendar, err)
	}
}

func TestSystemdTimerPersistent(t *testing.T) {
	dir := t.TempDir()
	writeUnit(t, dir, "foo.timer", "[Timer]\nOnCalendar=daily\nPersistent=true\n")
	desc, _ := sockConvert(t, dir, sockSvc)
	if !desc.StartOnCalendarPersistent {
		t.Error("Persistent=true not carried")
	}
}

// A service its [Install] has wanted by a target is started at boot as
// well in systemd: no manual = yes.
func TestSystemdTimerInstalledServiceNotManual(t *testing.T) {
	dir := t.TempDir()
	writeUnit(t, dir, "foo.timer", "[Timer]\nOnCalendar=daily\n")
	desc, warns := sockConvert(t, dir, sockSvc+"\n[Install]\nWantedBy=multi-user.target\n")
	if desc.StartOnCalendar != "daily" || desc.ManualStart || !sockWarned(warns, "at boot when enabled") {
		t.Errorf("calendar %q manual %v warns %v", desc.StartOnCalendar, desc.ManualStart, warns)
	}
}

// The timer's conditions become the service's when only the calendar
// starts it (fstrim.timer: ConditionVirtualization=!container).
func TestSystemdTimerConditions(t *testing.T) {
	dir := t.TempDir()
	writeUnit(t, dir, "foo.timer", "[Unit]\nConditionVirtualization=!container\nConditionPathExists=|/x\n[Timer]\nOnCalendar=weekly\n")
	desc, warns := sockConvert(t, dir, "[Unit]\nConditionPathExists=/etc/foo\nConditionVirtualization=!container\n"+sockSvc)
	got := predParams(desc)
	if n := strings.Count(got, "container"); n != 1 {
		t.Errorf("the check the timer and the service share is there %d times: %q", n, got)
	}
	if !strings.Contains(got, "container") || !strings.Contains(got, "/etc/foo") || strings.Contains(got, "/x") ||
		!sockWarned(warns, "triggering") {
		t.Errorf("conditions %q warns %v", got, warns)
	}
	dir = t.TempDir()
	writeUnit(t, dir, "foo.timer", "[Unit]\nConditionVirtualization=!container\n[Timer]\nOnCalendar=weekly\n")
	desc, warns = sockConvert(t, dir, sockSvc+"\n[Install]\nWantedBy=multi-user.target\n")
	if strings.Contains(predParams(desc), "container") || !sockWarned(warns, "also starts at boot") {
		t.Errorf("installed: conditions %q warns %v", predParams(desc), warns)
	}
}

func predParams(d *ServiceDescription) string {
	var s []string
	for _, p := range d.Predicates {
		s = append(s, p.Param)
	}
	return strings.Join(s, "|")
}

func TestSystemdTimerRandomizedDelay(t *testing.T) {
	dir := t.TempDir()
	writeUnit(t, dir, "foo.timer", "[Timer]\nOnCalendar=daily\nRandomizedDelaySec=100min\n")
	desc, _ := sockConvert(t, dir, sockSvc)
	if desc.StartOnCalendarRandomizedDelay != 100*time.Minute {
		t.Errorf("delay %v", desc.StartOnCalendarRandomizedDelay)
	}
}

// An empty Condition in the timer's drop-in empties the timer's list.
func TestSystemdTimerConditionReset(t *testing.T) {
	dir := t.TempDir()
	writeUnit(t, dir, "foo.timer", "[Unit]\nConditionVirtualization=!container\nAssertPathExists=/etc/a\n[Timer]\nOnCalendar=weekly\n")
	os.MkdirAll(filepath.Join(dir, "foo.timer.d"), 0o755)
	os.WriteFile(filepath.Join(dir, "foo.timer.d", "o.conf"), []byte("[Unit]\nConditionVirtualization=\n"), 0o644)
	desc, _ := sockConvert(t, dir, sockSvc)
	if got := predParams(desc); strings.Contains(got, "container") || !strings.Contains(got, "/etc/a") {
		t.Errorf("after the drop-in's reset: %q", got)
	}
}

// OnBootSec= / OnStartupSec= / OnUnitActiveSec= are the monotonic
// timers; several of one kind keep the earliest; an empty one resets
// the timer's list, calendar times included.
func TestSystemdTimerMonotonic(t *testing.T) {
	dir := t.TempDir()
	writeUnit(t, dir, "foo.timer", "[Timer]\nOnBootSec=15min\nOnStartupSec=10min\nOnUnitActiveSec=1d\n")
	desc, warns := sockConvert(t, dir, sockSvc)
	if desc.StartOnBootSec != 10*time.Minute || desc.StartOnUnitActiveSec != 24*time.Hour || !desc.ManualStart ||
		desc.StartOnCalendar != "" || !sockWarned(warns, "start-on-unit-active-sec") {
		t.Errorf("boot %v active %v manual %v calendar %q warns %v", desc.StartOnBootSec, desc.StartOnUnitActiveSec,
			desc.ManualStart, desc.StartOnCalendar, warns)
	}
	dir = t.TempDir()
	writeUnit(t, dir, "foo.timer", "[Timer]\nOnCalendar=daily\nOnBootSec=15min\nOnBootSec=\nOnUnitActiveSec=2h\n")
	desc, _ = sockConvert(t, dir, sockSvc)
	if desc.StartOnCalendar != "" || desc.StartOnBootSec != 0 || desc.StartOnUnitActiveSec != 2*time.Hour {
		t.Errorf("after the reset: calendar %q boot %v active %v", desc.StartOnCalendar, desc.StartOnBootSec, desc.StartOnUnitActiveSec)
	}
}
