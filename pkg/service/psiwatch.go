package service

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// PSI window is fixed at 2s (matches systemd's MemoryPressureThresholdSec
// default window). Only the stall threshold is configurable per service.
const psiWindowUsec = 2_000_000

// psiDefaultThreshold is the stall time used when a *-watch key is
// enabled but the matching *-threshold key is left unset. Mirrors
// systemd's default of 200ms within a 2s window.
const psiDefaultThreshold = 200 * time.Millisecond

// psiWatcher runs one goroutine per service that has any pressure watch
// enabled. It multiplexes up to three fds (mem/cpu/io) into a single
// poll(2) loop and emits a SERVICEEVENT per resource when the kernel
// signals POLLPRI on that fd.
//
// wake is an eventfd in the same poll set. Cancelling writes to it so the
// goroutine closes the trigger fds at once. It used to notice only on a
// 500ms poll timeout, which kept the triggers alive past the cgroup's
// rmdir in Stopped(). Destroying a trigger costs the kernel a
// synchronize_rcu, 4-8ms on a 12-CPU VM, and rmdir waits for it: every
// stop of a watched service paid it, under the queue lock. Closing when
// the stop begins lets the kernel do it while the process exits.
//
// The goroutine can also end by itself, and then closes wake: mu and closed
// keep a later cancel from writing to an fd number reused by another file.
type psiWatcher struct {
	mu     sync.Mutex
	wake   int
	closed bool
}

// armPSIWatcher opens the configured pressure files, writes the trigger
// spec, and starts the poll loop. No-op when no pressure watch is
// enabled or the service has no cgroup path (pressure files are inside
// the cgroup).
func (sr *ServiceRecord) armPSIWatcher() {
	sr.cancelPSIWatcher()
	if !sr.psiMemWatch && !sr.psiCPUWatch && !sr.psiIOWatch {
		return
	}
	cgPath := sr.EffectiveCgroupPath()
	if cgPath == "" {
		return
	}

	type entry struct {
		event    ServiceEvent
		resource string
		fd       int
	}
	var entries []entry
	open := func(watch bool, threshold time.Duration, filename, resource string, event ServiceEvent) {
		if !watch {
			return
		}
		if threshold <= 0 {
			threshold = psiDefaultThreshold
		}
		fd, err := openPSITrigger(cgPath+"/"+filename, threshold)
		if err != nil {
			sr.services.logger.Error(
				"Service '%s': %s pressure watch disabled: %v",
				sr.serviceName, resource, err)
			return
		}
		entries = append(entries, entry{event: event, resource: resource, fd: fd})
	}
	open(sr.psiMemWatch, sr.psiMemThr, "memory.pressure", "memory", EventPressureMemory)
	open(sr.psiCPUWatch, sr.psiCPUThr, "cpu.pressure", "cpu", EventPressureCPU)
	open(sr.psiIOWatch, sr.psiIOThr, "io.pressure", "io", EventPressureIO)
	if len(entries) == 0 {
		return
	}

	wake, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		for _, e := range entries {
			unix.Close(e.fd)
		}
		sr.services.logger.Error("Service '%s': pressure watch disabled: eventfd: %v",
			sr.serviceName, err)
		return
	}
	w := &psiWatcher{wake: wake}
	sr.psiWatch = w

	svc := sr.self
	set := sr.services
	name := sr.serviceName

	go func() {
		defer func() {
			for _, e := range entries {
				unix.Close(e.fd)
			}
			w.mu.Lock()
			unix.Close(wake)
			w.closed = true
			w.mu.Unlock()
		}()
		// The eventfd goes last, so fds[i] stays entries[i].
		fds := make([]unix.PollFd, len(entries)+1)
		for i, e := range entries {
			fds[i] = unix.PollFd{Fd: int32(e.fd), Events: unix.POLLPRI}
		}
		fds[len(entries)] = unix.PollFd{Fd: int32(wake), Events: unix.POLLIN}
		for {
			// Reset revents in case the kernel left stale bits (some
			// architectures reuse the field between calls).
			for i := range fds {
				fds[i].Revents = 0
			}
			_, err := unix.Poll(fds, -1)
			if err != nil {
				if errors.Is(err, syscall.EINTR) {
					continue
				}
				return
			}
			if fds[len(entries)].Revents != 0 {
				return
			}
			for i, pfd := range fds[:len(entries)] {
				if pfd.Revents&unix.POLLPRI == 0 {
					continue
				}
				e := entries[i]
				// Notify listeners under the queue lock, matching the
				// state-machine ServiceEvent path. Re-check that the
				// service is still STARTED — a stop may have raced us
				// between the kernel event and this notify.
				set.queueMu.Lock()
				if svc.State() != StateStarted {
					set.queueMu.Unlock()
					return
				}
				set.logger.Info(
					"Service '%s': %s pressure threshold crossed",
					name, e.resource)
				sr := svc.Record()
				sr.notifyListeners(e.event)
				set.queueMu.Unlock()
			}
		}
	}()
}

// cancelPSIWatcher stops the watcher goroutine if armed. The goroutine
// closes its own fds, the eventfd included, on exit; the write only
// wakes it, so this never waits for the kernel's trigger teardown.
func (sr *ServiceRecord) cancelPSIWatcher() {
	if w := sr.psiWatch; w != nil {
		w.mu.Lock()
		if !w.closed {
			var one [8]byte
			one[0] = 1 // eventfd counters are host-endian; 1 is nonzero either way
			_, _ = unix.Write(w.wake, one[:])
		}
		w.mu.Unlock()
		sr.psiWatch = nil
	}
}

// openPSITrigger opens a cgroup v2 pressure file O_RDWR|O_NONBLOCK and
// writes the trigger spec ("some THRESHOLD_US WINDOW_US"). Returns the
// fd on success. The fd must be kept open — the kernel removes the
// trigger when its last reference goes away.
//
// Threshold must fall within [500us, window]; anything smaller is
// clamped up. Fails cleanly on kernels without PSI support (ENOENT)
// or when the resource has no trigger support (EINVAL on write).
func openPSITrigger(path string, threshold time.Duration) (int, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open %s: %w", path, err)
	}
	usec := int64(threshold / time.Microsecond)
	if usec < 500 {
		usec = 500
	}
	if usec > psiWindowUsec {
		usec = psiWindowUsec
	}
	spec := fmt.Sprintf("some %d %d", usec, psiWindowUsec)
	if _, err := unix.Write(fd, []byte(spec)); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("write trigger to %s: %w", path, err)
	}
	return fd, nil
}

// psiCheckSupport is a debug helper: returns nil when the kernel has
// PSI enabled and the cgroup v2 unified hierarchy exposes a pressure
// file at the given path. Unused at runtime but kept for tests.
func psiCheckSupport(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return f.Close()
}
