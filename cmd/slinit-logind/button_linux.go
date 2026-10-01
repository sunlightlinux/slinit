package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

// The lid and power-button watcher — elogind's logind-button.c.
//
// Without this, closing a laptop's lid did nothing: the Handle*
// properties reported "ignore" because there was nothing to report. They
// now report what the config asks for, and this file is what makes that
// true.
//
// Everything is driven from evdev rather than ACPI procfs. /proc/acpi is
// a state file, good for answering "is the lid shut right now" (which is
// what the LidClosed property still uses) and useless for "the lid just
// shut" — there is no event to wait on. evdev delivers the transition.

// ioctl encoding. Linux's _IOC packs direction, type, number and size
// into one word; x/sys/unix exposes helpers for the common fixed-size
// cases but not for EVIOCGBIT, whose size is part of the request. Same
// hand-rolled approach as the DRM master constants in syscalls_linux.go.
const (
	iocRead      = 2
	iocNRShift   = 0
	iocTypeShift = 8
	iocSizeShift = 16
	iocDirShift  = 30
)

func iocR(typ, nr, size uintptr) uintptr {
	return (iocRead << iocDirShift) | (size << iocSizeShift) |
		(typ << iocTypeShift) | (nr << iocNRShift)
}

// evdev event types and codes, from linux/input-event-codes.h.
const (
	evKey = 0x01
	evSW  = 0x05

	swLID  = 0x00
	swDOCK = 0x05

	keyPower   = 116
	keySleep   = 142
	keySuspend = 205

	// Enough bits for every code in either type we query.
	evBitmapBytes = 96
)

// evdevEvent mirrors struct input_event. unix.Timeval keeps the layout
// right on both 32- and 64-bit, where a hand-written int64 pair would
// silently mis-size the struct on one of them.
type evdevEvent struct {
	Time  unix.Timeval
	Type  uint16
	Code  uint16
	Value int32
}

var evdevEventSize = int(unsafe.Sizeof(evdevEvent{}))

// buttonWatcher reads the input devices that can report a lid switch or
// a power/sleep key and applies the configured action.
type buttonWatcher struct {
	m   *manager
	cfg buttonConfig

	mu sync.Mutex
	// holdoffUntil suppresses lid handling. Set at start and after every
	// wake: a lid switch that still reads "closed" as the machine
	// resumes would otherwise suspend it again at once, which an
	// operator experiences as a laptop that will not wake up.
	holdoffUntil time.Time

	stop chan struct{}
}

func newButtonWatcher(m *manager, cfg buttonConfig) *buttonWatcher {
	return &buttonWatcher{m: m, cfg: cfg, stop: make(chan struct{})}
}

// enabled reports whether any handler is configured. With everything on
// "ignore" the devices are not opened at all — no reason to hold file
// descriptors on every input node to then discard what they say.
func (w *buttonWatcher) enabled() bool {
	for _, a := range []buttonAction{
		w.cfg.PowerKey, w.cfg.SuspendKey, w.cfg.HibernateKey,
		w.cfg.LidSwitch, w.cfg.LidSwitchExternalPower, w.cfg.LidSwitchDocked,
	} {
		if a != actionIgnore {
			return true
		}
	}
	return false
}

// Start begins watching. Returns immediately; the work happens in a
// goroutine. A no-op when nothing is configured.
func (w *buttonWatcher) Start() {
	if !w.enabled() {
		return
	}
	w.armHoldoff()
	go w.run()
}

func (w *buttonWatcher) Stop() { close(w.stop) }

// armHoldoff starts the holdoff window.
func (w *buttonWatcher) armHoldoff() {
	w.mu.Lock()
	w.holdoffUntil = time.Now().Add(w.cfg.HoldoffTimeout)
	w.mu.Unlock()
}

func (w *buttonWatcher) inHoldoff() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return time.Now().Before(w.holdoffUntil)
}

// run is the event loop. Devices are re-scanned periodically rather than
// followed with a udev netlink subscription: a USB keyboard with a power
// key, or a dock appearing, is rare enough that noticing within a few
// seconds is indistinguishable from instantly, and a netlink monitor is
// a lot of machinery to maintain for that.
func (w *buttonWatcher) run() {
	const rescanEvery = 10 * time.Second
	var (
		files    []*os.File
		lastScan time.Time
	)
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()

	for {
		select {
		case <-w.stop:
			return
		default:
		}

		if time.Since(lastScan) >= rescanEvery {
			for _, f := range files {
				_ = f.Close()
			}
			files = w.openInterestingDevices()
			lastScan = time.Now()
			if w.m != nil && w.m.debug {
				fmt.Fprintf(os.Stderr, "slinit-logind: button watcher following %d device(s)\n",
					len(files))
			}
		}
		if len(files) == 0 {
			// Nothing to watch yet — wait out the rescan interval
			// instead of spinning.
			select {
			case <-w.stop:
				return
			case <-time.After(rescanEvery):
			}
			continue
		}

		pfds := make([]unix.PollFd, 0, len(files))
		for _, f := range files {
			pfds = append(pfds, unix.PollFd{Fd: int32(f.Fd()), Events: unix.POLLIN})
		}
		// A bounded timeout so the rescan happens and stop is noticed
		// even on a machine where nothing is ever pressed.
		n, err := unix.Poll(pfds, 1000)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return
		}
		if n == 0 {
			continue
		}
		for i, pfd := range pfds {
			if pfd.Revents&unix.POLLIN != 0 {
				w.drain(files[i])
			}
			if pfd.Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
				// Device went away; force a rescan.
				lastScan = time.Time{}
			}
		}
	}
}

// drain reads whatever events are queued on one device.
func (w *buttonWatcher) drain(f *os.File) {
	buf := make([]byte, evdevEventSize*16)
	for {
		n, err := unix.Read(int(f.Fd()), buf)
		if n <= 0 || err != nil {
			return
		}
		for off := 0; off+evdevEventSize <= n; off += evdevEventSize {
			ev := (*evdevEvent)(unsafe.Pointer(&buf[off]))
			w.handle(ev.Type, ev.Code, ev.Value)
		}
		if n < len(buf) {
			return
		}
	}
}

// handle applies one event.
func (w *buttonWatcher) handle(typ, code uint16, value int32) {
	switch typ {
	case evSW:
		if code != swLID {
			return
		}
		if value == 0 {
			// Lid opened. Not an action, but the machine is awake and
			// being used, so clear the holdoff.
			w.mu.Lock()
			w.holdoffUntil = time.Time{}
			w.mu.Unlock()
			return
		}
		w.lidClosed()
	case evKey:
		// Key down only. Acting on the release as well would run the
		// action twice.
		if value != 1 {
			return
		}
		switch code {
		case keyPower:
			w.act(w.cfg.PowerKey, "handle-power-key", "power key")
		case keySleep:
			w.act(w.cfg.SuspendKey, "handle-suspend-key", "sleep key")
		case keySuspend:
			w.act(w.cfg.HibernateKey, "handle-hibernate-key", "suspend key")
		}
	}
}

// lidClosed resolves which lid setting applies and acts on it.
func (w *buttonWatcher) lidClosed() {
	if w.inHoldoff() {
		if w.m != nil && w.m.debug {
			fmt.Fprintf(os.Stderr, "slinit-logind: lid closed inside the holdoff window; ignoring\n")
		}
		return
	}
	action := w.cfg.lidAction(w.docked(), onExternalPower())
	w.act(action, "handle-lid-switch", "lid close")
}

// docked reports whether any input device says the machine is docked.
// Checked live rather than cached: docking is exactly the thing that
// changes between one lid close and the next.
func (w *buttonWatcher) docked() bool {
	paths, _ := filepath.Glob("/dev/input/event*")
	for _, p := range paths {
		f, err := os.OpenFile(p, os.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			continue
		}
		state, ok := switchState(f, swDOCK)
		_ = f.Close()
		if ok && state {
			return true
		}
	}
	return false
}

// act runs one action, after checking nothing has inhibited it.
func (w *buttonWatcher) act(action buttonAction, inhibitClass, what string) {
	if action == actionIgnore {
		return
	}
	if w.m != nil && w.m.inhibitors != nil {
		if in := w.m.inhibitors.blockedBy(inhibitClass); in != nil {
			fmt.Fprintf(os.Stderr, "slinit-logind: %s ignored — %s inhibited by %q: %s\n",
				what, inhibitClass, in.Who, in.Why)
			return
		}
	}

	fmt.Fprintf(os.Stderr, "slinit-logind: %s → %s\n", what, action)

	var err *dbus.Error
	switch action {
	case actionLock:
		err = w.m.lockAllSessions(true)
	case actionPoweroff:
		err = runShutdown("poweroff")
	case actionReboot:
		err = runShutdown("reboot")
	case actionHalt:
		err = runShutdown("halt")
	case actionSuspend:
		err = w.m.sleep(sleepVerb{"Suspend", "mem"})
	case actionHibernate:
		err = w.m.sleep(sleepVerb{"Hibernate", "disk"})
	case actionHybridSleep:
		err = w.m.sleep(sleepVerb{"HybridSleep", "disk"})
	case actionSuspendThenHibernate:
		err = w.m.sleep(sleepVerb{"SuspendThenHibernate", "mem"})
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit-logind: %s action %s failed: %v\n", what, action, err)
	}

	// Anything that put the machine to sleep has now returned, which
	// means it woke up. Re-arm the holdoff so a lid switch that still
	// reads closed does not send it straight back.
	switch action {
	case actionSuspend, actionHibernate, actionHybridSleep, actionSuspendThenHibernate:
		w.armHoldoff()
	}
}

// openInterestingDevices returns the input devices that can report a lid
// switch or one of the power keys. Opened read-only and non-blocking:
// this watcher never writes, and a blocking read on an idle node would
// wedge the loop.
func (w *buttonWatcher) openInterestingDevices() []*os.File {
	paths, _ := filepath.Glob("/dev/input/event*")
	var out []*os.File
	for _, p := range paths {
		f, err := os.OpenFile(p, os.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			continue
		}
		if w.interesting(f) {
			out = append(out, f)
			continue
		}
		_ = f.Close()
	}
	return out
}

// interesting reports whether a device advertises any code this watcher
// acts on. Asking the device beats matching on names: "Lid Switch" is
// conventional, not guaranteed, and a USB keyboard's power key lives on
// a node called anything at all.
func (w *buttonWatcher) interesting(f *os.File) bool {
	if w.lidConfigured() && hasEventCode(f, evSW, swLID) {
		return true
	}
	if w.cfg.PowerKey != actionIgnore && hasEventCode(f, evKey, keyPower) {
		return true
	}
	if w.cfg.SuspendKey != actionIgnore && hasEventCode(f, evKey, keySleep) {
		return true
	}
	if w.cfg.HibernateKey != actionIgnore && hasEventCode(f, evKey, keySuspend) {
		return true
	}
	return false
}

func (w *buttonWatcher) lidConfigured() bool {
	return w.cfg.LidSwitch != actionIgnore ||
		w.cfg.LidSwitchExternalPower != actionIgnore ||
		w.cfg.LidSwitchDocked != actionIgnore
}

// hasEventCode asks the device whether it can emit `code` of `typ`,
// via EVIOCGBIT.
func hasEventCode(f *os.File, typ, code uint16) bool {
	bitmap := make([]byte, evBitmapBytes)
	req := iocR('E', uintptr(0x20+typ), uintptr(len(bitmap)))
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), req,
		uintptr(unsafe.Pointer(&bitmap[0]))); errno != 0 {
		return false
	}
	return bitSet(bitmap, code)
}

// switchState reads the current value of a switch via EVIOCGSW. The
// second return reports whether the device could be asked at all, so a
// node that has no switches is not mistaken for one reporting "off".
func switchState(f *os.File, code uint16) (on bool, ok bool) {
	bitmap := make([]byte, evBitmapBytes)
	req := iocR('E', 0x1b, uintptr(len(bitmap)))
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), req,
		uintptr(unsafe.Pointer(&bitmap[0]))); errno != 0 {
		return false, false
	}
	return bitSet(bitmap, code), true
}

func bitSet(bitmap []byte, bit uint16) bool {
	idx := int(bit) / 8
	if idx >= len(bitmap) {
		return false
	}
	return bitmap[idx]&(1<<(bit%8)) != 0
}
