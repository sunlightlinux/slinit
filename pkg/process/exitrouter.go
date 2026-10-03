package process

import (
	"sync"
	"syscall"
	"time"
)

// ExitRouter routes wait status for a managed child PID from whichever
// caller learns it first to the goroutine that owns the child.
//
// Background: when slinit runs as PID 1 it must call Wait4(-1, ...) in its
// SIGCHLD handler to reap orphaned grandchildren (double-forked daemons,
// setsid'd shells, etc.). That syscall, however, also collects the status
// of any *managed* child that exits at the same moment — racing against
// the per-service goroutine's cmd.Wait() (which uses Wait4(pid, 0)). When
// the orphan reaper wins, the per-service goroutine's Wait4 returns
// ECHILD, cmd.Wait() yields a non-ExitError, and the status defaults to
// the zero WaitStatus — i.e. "exited cleanly with code 0", silently
// losing the real exit code.
//
// The router closes this race deterministically: the per-service
// goroutine registers its pid before fork, then selects on both its own
// cmd.Wait() AND the router-delivered status. The orphan reaper calls
// Route(pid, status) for every pid it reaps; if the pid was registered
// the goroutine receives the real status, if not the reap was a true
// orphan and is handled by reapOrphans' fallback logging.
// The doc above used to say the per-service goroutine "registers its pid
// before fork". It cannot: there is no pid before the fork. Registration
// happens just after cmd.Start(), and in that window a child that exits
// immediately could be reaped by Wait4(-1) with nobody registered — Route
// found no entry, discarded the status, and cmd.Wait() then reported
// ECHILD, which defaulted to "exited cleanly with 0". A `type = scripted`
// service running /bin/false was therefore sometimes reported STARTED,
// and a failing dependency stopped blocking its dependents.
//
// Measured at roughly 6 losses in 3000 starts with a hot reaper — rare
// enough to look like a flaky test and frequent enough to matter on a
// machine that starts services all day. So an unclaimed status is now
// held briefly instead of thrown away, and a Register that arrives after
// the reap collects it.
type ExitRouter struct {
	mu sync.Mutex
	m  map[int]chan syscall.WaitStatus

	// pending holds statuses reaped before anyone registered. Entries
	// are consumed by a late Register, and otherwise expire: most of
	// them belong to genuine orphans — double-forked daemons, setsid'd
	// shells — that nobody will ever register, and on PID 1 those arrive
	// for the life of the machine.
	pending map[int]pendingExit
}

// pendingExit is a reaped status waiting for its registration.
type pendingExit struct {
	status syscall.WaitStatus
	at     time.Time
}

// pendingTTL is how long an unclaimed status is kept.
//
// Registration follows cmd.Start() by a few instructions, so anything
// beyond a few milliseconds is already generous; a second is chosen to
// stay correct on a machine so loaded that the forking goroutine is
// descheduled, while still being far too short to accumulate.
const pendingTTL = time.Second

// pendingMax bounds the table even if time somehow stops behaving — a
// backstop, not the primary mechanism. One slot is ~40 bytes, so this
// cannot matter, and it means a stuck clock cannot turn a debug aid into
// a leak inside PID 1.
const pendingMax = 1024

// NewExitRouter returns an empty router. Most callers should use the
// process-wide DefaultExitRouter; this constructor exists for tests.
func NewExitRouter() *ExitRouter {
	return &ExitRouter{
		m:       make(map[int]chan syscall.WaitStatus),
		pending: make(map[int]pendingExit),
	}
}

// Register declares that pid is managed and returns a buffered channel
// that will receive the child's WaitStatus if the orphan reaper learns
// it before cmd.Wait() does. Channel is cap 1 so Route's send is never
// blocking. Caller must Unregister when the wait goroutine is done.
func (r *ExitRouter) Register(pid int) <-chan syscall.WaitStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch := make(chan syscall.WaitStatus, 1)

	// The child may already have exited and been reaped in the few
	// instructions since cmd.Start() returned. If so its status is
	// waiting here, and handing it over now is the whole point: the
	// alternative is cmd.Wait() returning ECHILD and the exit code
	// becoming a silent zero.
	if p, ok := r.pending[pid]; ok {
		delete(r.pending, pid)
		ch <- p.status
		return ch
	}

	r.m[pid] = ch
	return ch
}

// Unregister removes pid from the routing table. Idempotent — safe to
// call even if Route already consumed the entry.
func (r *ExitRouter) Unregister(pid int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, pid)
}

// Route delivers status to the goroutine waiting on pid. Returns true if
// the pid was registered (status delivered to a managed-child waiter),
// false if the pid is unknown to slinit (a real orphan — caller should
// continue with its own handling).
//
// The entry is deleted on first delivery so a subsequent reap of the
// same pid number (after pid reuse) cannot replay an old status into a
// fresh registration.
func (r *ExitRouter) Route(pid int, status syscall.WaitStatus) bool {
	r.mu.Lock()
	ch, ok := r.m[pid]
	if ok {
		delete(r.m, pid)
	} else {
		// Nobody is waiting yet. Either this is a genuine orphan, or it
		// is a managed child that exited before its registration landed —
		// and the two are indistinguishable from here, so the status is
		// held rather than guessed about.
		r.prunePendingLocked()
		r.pending[pid] = pendingExit{status: status, at: time.Now()}
	}
	r.mu.Unlock()
	if !ok {
		return false
	}
	// Channel is cap 1 and freshly allocated per Register: the send
	// cannot block. The default branch guards against a paranoid case
	// where someone else somehow consumed it (e.g. test injection).
	select {
	case ch <- status:
	default:
	}
	return true
}

// DefaultExitRouter is the process-wide router used by StartProcess and
// the PID-1 SIGCHLD handler. Code paths that don't run inside slinit's
// init binary (unit tests, slinit-runner) simply never call Route, so
// every Register/Unregister pair is a no-op cost.
var DefaultExitRouter = NewExitRouter()

// prunePendingLocked drops expired entries, and if the table is somehow
// still at its cap, the oldest one. Caller holds r.mu.
func (r *ExitRouter) prunePendingLocked() {
	now := time.Now()
	for pid, p := range r.pending {
		if now.Sub(p.at) > pendingTTL {
			delete(r.pending, pid)
		}
	}
	for len(r.pending) >= pendingMax {
		oldestPID, oldest := 0, now
		for pid, p := range r.pending {
			if !p.at.After(oldest) {
				oldestPID, oldest = pid, p.at
			}
		}
		delete(r.pending, oldestPID)
	}
}

// PendingLen reports how many reaped statuses are held waiting for a
// registration. For tests and for anyone wondering whether the table
// bounds itself.
func (r *ExitRouter) PendingLen() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending)
}
