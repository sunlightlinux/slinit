// Manager properties — org.freedesktop.login1.Manager's property dict.
//
// elogind exposes 52 properties here; until this file landed we served
// zero, which is why `loginctl show` printed nothing and every desktop
// stack that reads power/lid/inhibitor state off the Manager fell back
// to its own defaults.
//
// These are deliberately NOT built on godbus's prop.Export the way the
// Session / User / Seat objects are. prop.Prop stores a snapshot value
// and only re-reads it when something calls SetMust; most of what lives
// here (session counts, AC state, lid state) changes underneath us with
// no event to hang an update on. A getter-per-property table computes
// on demand instead, which keeps the daemon free of a polling loop.
//
// Honesty rule for the config-shaped properties: where elogind reports
// what its logind.conf *would* do, we report what slinit-logind
// *actually* does. Every Handle*Key / HandleLidSwitch / IdleAction is
// therefore "ignore" — we have no evdev button watcher (elogind's
// logind-button.c) and no idle timer, so claiming "poweroff" would be a
// lie. It is also the functionally better answer: GNOME's
// settings-daemon and XFCE's power manager both check these to decide
// whether logind already owns the key, and "ignore" tells them to
// handle it themselves.
package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

// infinity is systemd's USEC_INFINITY / UINT64_MAX sentinel, used for
// "no limit" on the *USec and *Max properties.
const infinity = ^uint64(0)

// ScheduledShutdown is the (st) struct returned by the property of the
// same name: (type, usec). A named type is required — an anonymous
// []interface{} would be marshalled as `av`, which loginctl rejects.
type ScheduledShutdown struct {
	Type string
	USec uint64
}

// managerProp describes one entry of the Manager property dict.
//
// Sig is carried explicitly rather than inferred from Get's return so
// the table doubles as the contract we check against elogind's vtable
// (see TestManagerPropSignatures).
//
// Emit is the value of the EmitsChangedSignal annotation: "const" for
// values fixed for the daemon's lifetime, "true" for ones that emit
// PropertiesChanged, "false" for ones that change without notice.
// We use "false" wherever elogind uses EMITS_CHANGE but we don't have
// the machinery to fire the signal yet — telling a client to expect a
// signal that never arrives is worse than telling it to re-read.
type managerProp struct {
	Name     string
	Sig      string
	Emit     string
	Writable bool
	Get      func(m *manager) any
	Set      func(m *manager, v dbus.Variant) *dbus.Error
}

// managerPropSpec is in elogind's vtable order so a side-by-side diff
// against logind-dbus.c stays readable.
var managerPropSpec = []managerProp{
	{
		Name: "EnableWallMessages", Sig: "b", Emit: "false", Writable: true,
		Get: func(m *manager) any {
			m.mu.RLock()
			defer m.mu.RUnlock()
			return m.enableWallMessages
		},
		Set: func(m *manager, v dbus.Variant) *dbus.Error {
			b, ok := v.Value().(bool)
			if !ok {
				return prop.ErrInvalidArg
			}
			m.mu.Lock()
			m.enableWallMessages = b
			m.mu.Unlock()
			return nil
		},
	},
	{
		Name: "WallMessage", Sig: "s", Emit: "false", Writable: true,
		Get: func(m *manager) any {
			m.mu.RLock()
			defer m.mu.RUnlock()
			return m.wallMessage
		},
		Set: func(m *manager, v dbus.Variant) *dbus.Error {
			s, ok := v.Value().(string)
			if !ok {
				return prop.ErrInvalidArg
			}
			m.mu.Lock()
			m.wallMessage = s
			m.mu.Unlock()
			return nil
		},
	},

	// NAutoVTs is how many VTs logind pre-allocates getties on. slinit
	// manages getty services itself via its own service files, so we
	// allocate none — and report none.
	{Name: "NAutoVTs", Sig: "u", Emit: "const",
		Get: func(m *manager) any { return uint32(0) }},

	{Name: "KillOnlyUsers", Sig: "as", Emit: "const",
		Get: func(m *manager) any { return []string{} }},
	{Name: "KillExcludeUsers", Sig: "as", Emit: "const",
		Get: func(m *manager) any { return []string{"root"} }},
	// KillUserProcesses: TerminateUser walks the session cgroups, but
	// we don't reap leftover processes on plain logout the way
	// elogind's KillUserProcesses=yes does.
	{Name: "KillUserProcesses", Sig: "b", Emit: "const",
		Get: func(m *manager) any { return false }},

	// Reboot-target plumbing (firmware setup, boot loader menu/entry).
	// The matching Set*/Can* methods don't exist yet, so these report
	// the "nothing requested" values systemd uses: false, UINT64_MAX,
	// empty string, empty list.
	{Name: "RebootParameter", Sig: "s", Emit: "false",
		Get: func(m *manager) any { return "" }},
	{Name: "RebootToFirmwareSetup", Sig: "b", Emit: "false",
		Get: func(m *manager) any { return false }},
	{Name: "RebootToBootLoaderMenu", Sig: "t", Emit: "false",
		Get: func(m *manager) any { return infinity }},
	{Name: "RebootToBootLoaderEntry", Sig: "s", Emit: "false",
		Get: func(m *manager) any { return "" }},
	{Name: "BootLoaderEntries", Sig: "as", Emit: "const",
		Get: func(m *manager) any { return []string{} }},

	// Idle tracking. Session.SetIdleHint is accepted but not stored,
	// so the aggregate is always "not idle".
	{Name: "IdleHint", Sig: "b", Emit: "false",
		Get: func(m *manager) any { return false }},
	{Name: "IdleSinceHint", Sig: "t", Emit: "false",
		Get: func(m *manager) any { return uint64(0) }},
	{Name: "IdleSinceHintMonotonic", Sig: "t", Emit: "false",
		Get: func(m *manager) any { return uint64(0) }},

	// Inhibitor aggregation. Inhibit() hands out an fd but keeps no
	// registry, so nothing is ever blocked or delayed. The empty
	// string is what elogind returns when no inhibitor of that class
	// is held — clients parse it as a colon-separated "what" list.
	{Name: "BlockInhibited", Sig: "s", Emit: "false",
		Get: func(m *manager) any { return "" }},
	{Name: "BlockWeakInhibited", Sig: "s", Emit: "false",
		Get: func(m *manager) any { return "" }},
	{Name: "DelayInhibited", Sig: "s", Emit: "false",
		Get: func(m *manager) any { return "" }},
	// With no registry there is no delay to wait out; report 0 rather
	// than elogind's 5s so a client that honours the value doesn't
	// stall for a handshake we never perform.
	{Name: "InhibitDelayMaxUSec", Sig: "t", Emit: "const",
		Get: func(m *manager) any { return uint64(0) }},
	{Name: "UserStopDelayUSec", Sig: "t", Emit: "const",
		Get: func(m *manager) any { return uint64(0) }},

	{Name: "SleepOperation", Sig: "as", Emit: "const",
		Get: func(m *manager) any { return sleepOperations() }},

	// Hardware key + lid handling: see the honesty note at the top of
	// this file. All "ignore" until logind-button.c's equivalent lands.
	{Name: "HandlePowerKey", Sig: "s", Emit: "const",
		Get: func(m *manager) any { return "ignore" }},
	{Name: "HandlePowerKeyLongPress", Sig: "s", Emit: "const",
		Get: func(m *manager) any { return "ignore" }},
	{Name: "HandleRebootKey", Sig: "s", Emit: "const",
		Get: func(m *manager) any { return "ignore" }},
	{Name: "HandleRebootKeyLongPress", Sig: "s", Emit: "const",
		Get: func(m *manager) any { return "ignore" }},
	{Name: "HandleSuspendKey", Sig: "s", Emit: "const",
		Get: func(m *manager) any { return "ignore" }},
	{Name: "HandleSuspendKeyLongPress", Sig: "s", Emit: "const",
		Get: func(m *manager) any { return "ignore" }},
	{Name: "HandleHibernateKey", Sig: "s", Emit: "const",
		Get: func(m *manager) any { return "ignore" }},
	{Name: "HandleHibernateKeyLongPress", Sig: "s", Emit: "const",
		Get: func(m *manager) any { return "ignore" }},
	{Name: "HandleLidSwitch", Sig: "s", Emit: "const",
		Get: func(m *manager) any { return "ignore" }},
	{Name: "HandleLidSwitchExternalPower", Sig: "s", Emit: "const",
		Get: func(m *manager) any { return "ignore" }},
	{Name: "HandleLidSwitchDocked", Sig: "s", Emit: "const",
		Get: func(m *manager) any { return "ignore" }},
	{Name: "HandleSecureAttentionKey", Sig: "s", Emit: "const",
		Get: func(m *manager) any { return "ignore" }},
	{Name: "HoldoffTimeoutUSec", Sig: "t", Emit: "const",
		Get: func(m *manager) any { return uint64(0) }},

	{Name: "IdleAction", Sig: "s", Emit: "const",
		Get: func(m *manager) any { return "ignore" }},
	{Name: "IdleActionUSec", Sig: "t", Emit: "const",
		Get: func(m *manager) any { return infinity }},

	// Shutdown/sleep progress. These flip while the daemon coordinates
	// the transition; we perform no coordination (no PrepareForSleep /
	// PrepareForShutdown signal yet), so they're constantly false.
	{Name: "PreparingForShutdown", Sig: "b", Emit: "false",
		Get: func(m *manager) any { return false }},
	{Name: "PreparingForShutdownWithMetadata", Sig: "a{sv}", Emit: "false",
		Get: func(m *manager) any { return map[string]dbus.Variant{} }},
	{Name: "PreparingForSleep", Sig: "b", Emit: "false",
		Get: func(m *manager) any { return false }},

	// ScheduleShutdown / CancelScheduledShutdown aren't implemented,
	// so nothing is ever scheduled.
	{Name: "ScheduledShutdown", Sig: "(st)", Emit: "false",
		Get: func(m *manager) any { return ScheduledShutdown{} }},
	{Name: "DesignatedMaintenanceTime", Sig: "s", Emit: "const",
		Get: func(m *manager) any { return "" }},

	// Chassis state. Docked needs an evdev SW_DOCK watcher we don't
	// have; lid + AC are readable straight out of sysfs/procfs.
	{Name: "Docked", Sig: "b", Emit: "false",
		Get: func(m *manager) any { return false }},
	{Name: "LidClosed", Sig: "b", Emit: "false",
		Get: func(m *manager) any { return lidClosed() }},
	{Name: "OnExternalPower", Sig: "b", Emit: "false",
		Get: func(m *manager) any { return onExternalPower() }},

	// RemoveIPC: elogind defaults to yes and clears SysV IPC + POSIX
	// queues on last logout. We don't, so we say so.
	{Name: "RemoveIPC", Sig: "b", Emit: "const",
		Get: func(m *manager) any { return false }},

	// XDG_RUNTIME_DIR sizing. pam_rundir / user-runtime-dir mount the
	// tmpfs on Sunlight, not us, but the advertised figures are what
	// systemd would use (10% of RAM, one inode per 4 KiB) so a client
	// sizing a cache against them lands in the right ballpark.
	{Name: "RuntimeDirectorySize", Sig: "t", Emit: "const",
		Get: func(m *manager) any { return runtimeDirectorySize() }},
	{Name: "RuntimeDirectoryInodesMax", Sig: "t", Emit: "const",
		Get: func(m *manager) any { return runtimeDirectorySize() / 4096 }},

	{Name: "InhibitorsMax", Sig: "t", Emit: "const",
		Get: func(m *manager) any { return uint64(8192) }},
	{Name: "NCurrentInhibitors", Sig: "t", Emit: "false",
		Get: func(m *manager) any { return countStateFiles("inhibitors") }},
	{Name: "SessionsMax", Sig: "t", Emit: "const",
		Get: func(m *manager) any { return uint64(8192) }},
	{Name: "NCurrentSessions", Sig: "t", Emit: "false",
		Get: func(m *manager) any { return countStateFiles("sessions") }},

	// UserTasksMax is marked HIDDEN in elogind's vtable — kept for the
	// handful of clients that still Get it by name. Infinity means the
	// per-user TasksMax is not managed here.
	{Name: "UserTasksMax", Sig: "t", Emit: "const",
		Get: func(m *manager) any { return infinity }},
	{Name: "StopIdleSessionUSec", Sig: "t", Emit: "const",
		Get: func(m *manager) any { return infinity }},
}

// managerPropByName indexes the spec for Get/Set lookups.
var managerPropByName = func() map[string]*managerProp {
	out := make(map[string]*managerProp, len(managerPropSpec))
	for i := range managerPropSpec {
		out[managerPropSpec[i].Name] = &managerPropSpec[i]
	}
	return out
}()

// managerProperties serves org.freedesktop.DBus.Properties on the
// Manager path. Mirrors propsWrapper's empty-interface tolerance:
// loginctl calls GetAll("") to dump everything regardless of interface.
type managerProperties struct{ m *manager }

func (p *managerProperties) Get(ifaceName, name string) (dbus.Variant, *dbus.Error) {
	if ifaceName != "" && ifaceName != iface {
		return dbus.Variant{}, prop.ErrIfaceNotFound
	}
	mp, ok := managerPropByName[name]
	if !ok {
		return dbus.Variant{}, prop.ErrPropNotFound
	}
	return dbus.MakeVariant(mp.Get(p.m)), nil
}

func (p *managerProperties) GetAll(ifaceName string) (map[string]dbus.Variant, *dbus.Error) {
	if ifaceName != "" && ifaceName != iface {
		return nil, prop.ErrIfaceNotFound
	}
	out := make(map[string]dbus.Variant, len(managerPropSpec))
	for i := range managerPropSpec {
		mp := &managerPropSpec[i]
		out[mp.Name] = dbus.MakeVariant(mp.Get(p.m))
	}
	return out, nil
}

func (p *managerProperties) Set(ifaceName, name string, v dbus.Variant) *dbus.Error {
	if ifaceName != "" && ifaceName != iface {
		return prop.ErrIfaceNotFound
	}
	mp, ok := managerPropByName[name]
	if !ok {
		return prop.ErrPropNotFound
	}
	if !mp.Writable || mp.Set == nil {
		return prop.ErrReadOnly
	}
	return mp.Set(p.m, v)
}

// managerIntrospectProps renders the spec as introspection XML entries.
func managerIntrospectProps() []introspect.Property {
	out := make([]introspect.Property, 0, len(managerPropSpec))
	for i := range managerPropSpec {
		mp := &managerPropSpec[i]
		access := "read"
		if mp.Writable {
			access = "readwrite"
		}
		out = append(out, introspect.Property{
			Name:   mp.Name,
			Type:   mp.Sig,
			Access: access,
			Annotations: []introspect.Annotation{{
				Name:  "org.freedesktop.DBus.Property.EmitsChangedSignal",
				Value: mp.Emit,
			}},
		})
	}
	return out
}

// --- probes ---

// countStateFiles counts *.json records under stateRoot/<sub>. Used for
// NCurrentSessions / NCurrentInhibitors, which elogind answers from its
// in-memory hashmaps; our equivalent state lives on disk.
func countStateFiles(sub string) uint64 {
	files, _ := filepath.Glob(filepath.Join(stateRoot, sub, "*.json"))
	return uint64(len(files))
}

// lidClosed reports the ACPI lid state. elogind tracks this through a
// udev-matched evdev device and the SW_LID switch bit; the procfs node
// is the same information without the event stream, which is all a
// poll-on-Get property needs.
//
// Machines with no lid (desktops, VMs) have no such node — report open.
func lidClosed() bool {
	states, _ := filepath.Glob("/proc/acpi/button/lid/*/state")
	for _, p := range states {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if strings.Contains(string(b), "closed") {
			return true
		}
	}
	return false
}

// onExternalPower mirrors systemd's on_ac_power(): true when any mains
// supply is online. A machine that advertises no mains supply at all is
// assumed to be on AC — that's the desktop/VM case, and reporting
// "running on battery" there would make every desktop power applet
// switch to its low-power presentation.
func onExternalPower() bool {
	supplies, _ := filepath.Glob("/sys/class/power_supply/*")
	foundMains := false
	for _, s := range supplies {
		t, err := os.ReadFile(filepath.Join(s, "type"))
		if err != nil || strings.TrimSpace(string(t)) != "Mains" {
			continue
		}
		foundMains = true
		online, err := os.ReadFile(filepath.Join(s, "online"))
		if err == nil && strings.TrimSpace(string(online)) == "1" {
			return true
		}
	}
	return !foundMains
}

// sleepOperations lists the sleep verbs Manager.Sleep will accept, in
// preference order. Derived from what the kernel advertises in
// /sys/power/state rather than from config: the Sleep() implementation
// writes that file directly, so anything the kernel won't take is an
// operation we genuinely can't perform.
//
// "suspend-then-hibernate" and "hybrid-sleep" are deliberately absent —
// both need a timer or an image-then-suspend sequence that our
// single-write implementation doesn't do.
func sleepOperations() []string {
	out := []string{}
	if canSleep("mem") == "yes" {
		out = append(out, "suspend")
	}
	if canSleep("disk") == "yes" {
		out = append(out, "hibernate")
	}
	return out
}

// runtimeDirectorySize returns 10% of physical RAM, systemd's default
// XDG_RUNTIME_DIR tmpfs budget. Falls back to 128 MiB when /proc/meminfo
// is unreadable (container without a procfs mount).
func runtimeDirectorySize() uint64 {
	const fallback = 128 * 1024 * 1024
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return fallback
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			break
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			break
		}
		return kb * 1024 / 10
	}
	return fallback
}
