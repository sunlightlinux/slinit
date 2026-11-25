package service

import (
	"sync"
	"time"
)

// utmp/wtmp writes run on one goroutine, in the order they were asked
// for, never on the caller's. The create happens in startProcess under
// queueMu, and glibc's pututxline waits up to 10 seconds for a lock on
// utmp that any process able to open the file read-only can hold:
// measured, every getty start held queueMu for 10 s while a local user
// kept a read lock. The clear ran before queueMu but still stalled the
// service's monitor goroutine the same way.
//
// One FIFO goroutine keeps the order per pid: a create is queued before
// the goroutine that observes the process's exit is started, so its
// clear is always queued — and written — after it. The queue is
// unbounded: entries come from process starts and exits, which restart
// limits already bound, and blocking a caller here is what this exists
// to avoid.
type utmpQueue struct {
	mu      sync.Mutex
	jobs    []func()
	running bool
}

func (q *utmpQueue) push(job func()) {
	q.mu.Lock()
	q.jobs = append(q.jobs, job)
	start := !q.running
	q.running = true
	q.mu.Unlock()
	if start {
		go q.run()
	}
}

// run drains the queue and exits when it is empty; the next push
// starts a new one.
func (q *utmpQueue) run() {
	for {
		q.mu.Lock()
		if len(q.jobs) == 0 {
			q.running = false
			q.mu.Unlock()
			return
		}
		job := q.jobs[0]
		q.jobs[0] = nil
		q.jobs = q.jobs[1:]
		q.mu.Unlock()
		job()
	}
}

// flush waits until every job queued before it has run, or for timeout,
// and reports whether they all ran.
func (q *utmpQueue) flush(timeout time.Duration) bool {
	done := make(chan struct{})
	q.push(func() { close(done) })
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// FlushUtmp waits, at most timeout, for the utmp writes still queued and
// reports whether they all finished. The shutdown calls it before its own
// utmp/wtmp records and before the reboot or the soft-reboot exec, which
// would otherwise overtake or lose the clears of the services it stopped.
func (ss *ServiceSet) FlushUtmp(timeout time.Duration) bool {
	return ss.utmpQ.flush(timeout)
}

// utmpCreate queues OnUtmpCreate. Callers check HasUtmp first.
func (ss *ServiceSet) utmpCreate(id, line, mode string, pid int) {
	if f := ss.OnUtmpCreate; f != nil {
		ss.utmpQ.push(func() { f(id, line, mode, pid) })
	}
}

// utmpClear queues OnUtmpClear.
func (ss *ServiceSet) utmpClear(id, line string) {
	if f := ss.OnUtmpClear; f != nil {
		ss.utmpQ.push(func() { f(id, line) })
	}
}
