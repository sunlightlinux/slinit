package main

// Removing a session's cgroup scope once it has emptied.
//
// A scope can be removed only once no process is left in it, and when a
// session ends that is usually not yet the case: pam_slinit.so calls
// ReleaseSession from inside the session, from the login, sshd or
// display-manager process that is its leader, and TerminateSession's
// SIGKILL through cgroup.kill is delivered asynchronously. The rmdir then
// fails with EBUSY. It used to be left at that, with a comment promising
// a reaper that did not exist, so every console or ssh logout left an
// empty /sys/fs/cgroup/c<N> behind for good.
//
// The kernel reports a scope emptying as a change to its cgroup.events
// ("populated 0"), which inotify sees as IN_MODIFY (cgroup-v2.rst). So a
// scope the rmdir could not remove is watched there, and removed when it
// empties: right after the leader exits for a released session, or
// whenever the last process of a nohup'd job does. That is what systemd
// does with an abandoned scope, which stays until it is empty.

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

// scopeReaper removes the scopes that were still populated when their
// session ended. Its inotify descriptor and goroutine are made on first
// use, so a daemon whose scopes always empty in time holds neither, and
// made again after a failure (EMFILE, max_user_instances, a read error)
// rather than given up on for the life of the process.
type scopeReaper struct {
	mu      sync.Mutex
	fd      int
	file    *os.File // nil until the first watch, and after a failure
	closed  bool
	watches map[int32]string // watch descriptor -> scope

	// rmdir removes a scope, a variable for the tests.
	rmdir func(string) error
	dbgf  func(string, ...any)
}

func newScopeReaper(dbgf func(string, ...any)) *scopeReaper {
	return &scopeReaper{rmdir: rmdirScope, dbgf: dbgf, watches: map[int32]string{}}
}

// rmdirScope removes a scope and any child cgroups left inside it,
// deepest first, as systemd's cg_trim does: a process of the session may
// have made child cgroups of its own, and a scope with children cannot be
// removed even once they are all empty. A cgroup that still holds a
// process answers EBUSY, which stops the removal there.
func rmdirScope(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, e := range entries {
		// cgroupfs has no symlinks; a directory is a child cgroup.
		if e.IsDir() {
			if err := rmdirScope(filepath.Join(path, e.Name())); err != nil {
				return err
			}
		}
	}
	return unix.Rmdir(path)
}

// remove removes the scope now, or once it has emptied.
func (r *scopeReaper) remove(scope string) {
	err := r.rmdir(scope)
	if err == nil || errors.Is(err, unix.ENOENT) {
		return
	}
	if !errors.Is(err, unix.EBUSY) {
		r.debug("scope %s: cannot remove: %v", scope, err)
		return
	}
	if err := r.watch(scope); err != nil {
		r.debug("scope %s: still populated, cannot watch it: %v", scope, err)
	}
}

func (r *scopeReaper) debug(format string, args ...any) {
	if r.dbgf != nil {
		r.dbgf(format, args...)
	}
}

// watch arms the cgroup.events watch, then tries once more: the scope may
// have emptied between the rmdir and the watch, and that change would
// not be reported.
func (r *scopeReaper) watch(scope string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.watchLocked(scope)
}

func (r *scopeReaper) watchLocked(scope string) error {
	if err := r.startLocked(); err != nil {
		return err
	}
	wd, err := unix.InotifyAddWatch(r.fd, filepath.Join(scope, "cgroup.events"), unix.IN_MODIFY)
	if err != nil {
		return err
	}
	r.watches[int32(wd)] = scope
	r.retryLocked(int32(wd))
	return nil
}

// retryLocked tries the watched scope's removal again. A removed scope's
// watch goes with it (the kernel sends IN_IGNORED); one that went away
// some other way is dropped. r.mu is held.
func (r *scopeReaper) retryLocked(wd int32) {
	scope, ok := r.watches[wd]
	if !ok {
		return
	}
	err := r.rmdir(scope)
	switch {
	case err == nil:
		r.debug("scope %s: emptied, removed", scope)
		delete(r.watches, wd)
		// The kernel drops the watch with the inode; this covers a
		// test's rmdir, which removes nothing.
		_, _ = unix.InotifyRmWatch(r.fd, uint32(wd))
	case errors.Is(err, unix.EBUSY):
		// Still populated: keep waiting.
	default:
		r.debug("scope %s: cannot remove: %v", scope, err)
		delete(r.watches, wd)
		_, _ = unix.InotifyRmWatch(r.fd, uint32(wd))
	}
}

// startLocked makes the inotify descriptor and its reader if there is
// none. r.mu is held.
func (r *scopeReaper) startLocked() error {
	if r.closed {
		return os.ErrClosed
	}
	if r.file != nil {
		return nil
	}
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return err
	}
	r.fd = fd
	// Non-blocking, so Read parks in the netpoller rather than an OS
	// thread.
	r.file = os.NewFile(uintptr(fd), "inotify")
	go r.loop(r.file)
	return nil
}

func (r *scopeReaper) loop(f *os.File) {
	buf := make([]byte, 64*(unix.SizeofInotifyEvent+unix.NAME_MAX+1))
	for {
		n, err := f.Read(buf)
		if err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
				continue
			}
			r.restart(f, err)
			return
		}
		r.mu.Lock()
		for off := 0; off+unix.SizeofInotifyEvent <= n; {
			ev := (*unix.InotifyEvent)(unsafe.Pointer(&buf[off]))
			off += unix.SizeofInotifyEvent + int(ev.Len)
			switch {
			case ev.Mask&unix.IN_Q_OVERFLOW != 0:
				// Events were dropped, maybe the one a scope
				// emptied with: try every scope again.
				for wd := range r.watches {
					r.retryLocked(wd)
				}
			case ev.Mask&unix.IN_IGNORED != 0:
				delete(r.watches, ev.Wd)
			case ev.Mask&unix.IN_MODIFY != 0:
				r.retryLocked(ev.Wd)
			}
		}
		r.mu.Unlock()
	}
}

// restart replaces a descriptor that can no longer be read with a new one
// and watches its scopes again there, so they are not left until the
// next daemon start. Nothing is done once the reaper is closed.
func (r *scopeReaper) restart(f *os.File, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.file != f {
		return
	}
	r.debug("scope watch: %v; watching again", err)
	f.Close()
	r.file = nil
	scopes := make([]string, 0, len(r.watches))
	for _, scope := range r.watches {
		scopes = append(scopes, scope)
	}
	clear(r.watches)
	for _, scope := range scopes {
		if err := r.watchLocked(scope); err != nil {
			r.debug("scope %s: still populated, cannot watch it: %v", scope, err)
		}
	}
}

// close stops the reaper; for the tests.
func (r *scopeReaper) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.file != nil {
		r.file.Close()
		r.file = nil
	}
}

// pending is the number of scopes being waited on; for the tests.
func (r *scopeReaper) pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.watches)
}

// removeScope removes a session's scope now or once it has emptied.
//
// Only a scope where CreateSession makes them is accepted: directly under
// the cgroup root, named by a valid session id. The path comes from a
// session record, and a record naming, say, /sys/fs/cgroup/system.slice
// would otherwise have every empty cgroup below it removed, each time
// anything in that never-empty slice changed: a service cgroup slinit
// had just made, before moving the service into it, among them.
func (m *manager) removeScope(scope string) {
	c := filepath.Clean(scope)
	if filepath.Dir(c) != sessionCgroupRoot || !validSessionID(filepath.Base(c)) {
		if scope != "" {
			m.dbgf("scope %q: not a session scope, left alone", scope)
		}
		return
	}
	m.reaper().remove(c)
}

// reaper is the manager's scope reaper, made on first use. A test may
// set m.scopes beforehand.
func (m *manager) reaper() *scopeReaper {
	m.scopeOnce.Do(func() {
		if m.scopes == nil {
			m.scopes = newScopeReaper(m.dbgf)
		}
	})
	return m.scopes
}

// sessionCgroupRoot is where session scopes are made; a variable for the tests.
var sessionCgroupRoot = "/sys/fs/cgroup"

// sweepOrphanScopes removes the scopes of sessions that no longer exist.
// They are what a slinit-logind before this fix left at each logout, and
// what this one leaves when it is restarted while a scope it was waiting
// on is still populated, since the watch does not survive the restart.
//
// Only names this daemon hands out are touched: c<N> with N no greater
// than the session counter, and no record of a session by that name.
// rmdir removes nothing but an empty cgroup, and a populated one is
// watched like any other.
func (m *manager) sweepOrphanScopes() {
	b, err := os.ReadFile(filepath.Join(stateRoot, "next-session-id"))
	if err != nil {
		return
	}
	last, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return
	}
	entries, err := os.ReadDir(sessionCgroupRoot)
	if err != nil {
		return
	}
	sessionMu.Lock()
	defer sessionMu.Unlock()
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !orphanScopeName(name, last) || fileExists(sessionFile(name)) {
			continue
		}
		m.dbgf("scope %s: session gone, removing", name)
		m.reaper().remove(filepath.Join(sessionCgroupRoot, name))
	}
}

// orphanScopeName says whether name is a session id this daemon has
// handed out: "c" and a number from 1 to last, written as nextSessionID
// writes it.
func orphanScopeName(name string, last uint64) bool {
	digits, ok := strings.CutPrefix(name, "c")
	if !ok || digits == "" || digits[0] == '0' {
		return false
	}
	n, err := strconv.ParseUint(digits, 10, 64)
	return err == nil && n <= last && strconv.FormatUint(n, 10) == digits
}
