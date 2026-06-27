package process

import (
	"sync"
	"syscall"
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
type ExitRouter struct {
	mu sync.Mutex
	m  map[int]chan syscall.WaitStatus
}

// NewExitRouter returns an empty router. Most callers should use the
// process-wide DefaultExitRouter; this constructor exists for tests.
func NewExitRouter() *ExitRouter {
	return &ExitRouter{m: make(map[int]chan syscall.WaitStatus)}
}

// Register declares that pid is managed and returns a buffered channel
// that will receive the child's WaitStatus if the orphan reaper learns
// it before cmd.Wait() does. Channel is cap 1 so Route's send is never
// blocking. Caller must Unregister when the wait goroutine is done.
func (r *ExitRouter) Register(pid int) <-chan syscall.WaitStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch := make(chan syscall.WaitStatus, 1)
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
