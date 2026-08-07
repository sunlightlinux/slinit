package journal

import (
	"regexp"
	"sync"
	"time"
)

// DefaultBufferCap is the ring's default capacity if the caller passes
// 0 to NewBuffer. 4096 events cover ~5-10 minutes of steady-state
// logging on a small system (dozens of svcs, sub-Hz per-svc emit
// rate) and fit comfortably in ~2 MiB of Event structs including the
// map field and string interning overhead.
const DefaultBufferCap = 4096

// MinBufferCap is the floor. Anything below this is essentially
// useless — you'd lose events faster than a query can pull them.
const MinBufferCap = 32

// EventBuffer is a thread-safe, fixed-capacity ring of recent events.
// slinit's journal emit path pushes every event here in addition to
// (or instead of) sending it out the Unix socket, so slinit-journalctl
// can query events even when the persistent slinit-journald daemon is
// not running.
//
// The buffer is lossy: when full, oldest events are overwritten by
// new ones. This matches the runit/svlogd ring-buffer philosophy —
// bounded memory is more important than perfect completeness for a
// last-resort query surface. Long-term retention lives in the
// persistent journal files, not here.
//
// Access pattern:
//   - Slinit emit path holds a single writer.
//   - Multiple readers (control-socket handlers replying to
//     slinit-journalctl queries) call Snapshot concurrently.
// A sync.RWMutex would let us optimize the reader-heavy case, but
// Push has to hold the write lock briefly regardless and the ring is
// small enough that lock contention isn't a practical concern.
type EventBuffer struct {
	mu       sync.Mutex
	events   []*Event // circular; nil entries mean "unused slot"
	head     int      // next write position
	size     int      // number of stored events (<= capacity)
	capacity int
	// seq counts total events ever pushed. Callers can use it to
	// detect overrun between two Snapshot calls ("did I miss any
	// events since last check?").
	seq uint64
}

// NewEventBuffer creates a buffer of the given capacity. Zero selects
// DefaultBufferCap; values below MinBufferCap are silently promoted
// so operators typo-ing a config value don't get an unusable buffer.
func NewEventBuffer(capacity int) *EventBuffer {
	if capacity == 0 {
		capacity = DefaultBufferCap
	} else if capacity < MinBufferCap {
		capacity = MinBufferCap
	}
	return &EventBuffer{
		events:   make([]*Event, capacity),
		capacity: capacity,
	}
}

// Push adds an event to the ring. If the ring is full, the oldest
// event is silently evicted — the caller does not get an error, but
// Seq afterwards reflects the total count so overrun is detectable.
func (b *EventBuffer) Push(e *Event) {
	if e == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events[b.head] = e
	b.head = (b.head + 1) % b.capacity
	if b.size < b.capacity {
		b.size++
	}
	b.seq++
}

// Snapshot returns a copy of the current buffer contents in
// chronological order (oldest first). Returns a shallow copy of the
// []*Event slice — the events themselves are shared with the
// buffer, so callers should treat them as immutable.
//
// A separate seq value is returned so a follower can call Snapshot
// again later and check whether events were evicted between the two
// calls (seq2 - seq1 > returned event count).
func (b *EventBuffer) Snapshot() (events []*Event, seq uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	events = make([]*Event, 0, b.size)
	if b.size < b.capacity {
		// Ring hasn't wrapped yet: events are events[0..size-1] in
		// chronological order.
		events = append(events, b.events[:b.size]...)
	} else {
		// Ring is full: oldest is at head, newest at head-1.
		events = append(events, b.events[b.head:]...)
		events = append(events, b.events[:b.head]...)
	}
	return events, b.seq
}

// Len returns the current number of stored events.
func (b *EventBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.size
}

// Capacity returns the maximum number of events the ring can hold.
func (b *EventBuffer) Capacity() int { return b.capacity }

// Seq returns the total number of events ever Pushed. Overrun
// detection: if a reader saved Seq at time T1 and reads it again at
// T2, then (Seq(T2) - Seq(T1)) is the total count of events since
// T1; if that exceeds Capacity(), the reader missed some.
func (b *EventBuffer) Seq() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// QueryFilter narrows a Snapshot down to matching events. Nil fields
// mean "don't filter on this dimension." All non-nil filters are AND-
// combined; within a single dimension (e.g. multiple Units), matches
// are OR-combined ("unit is a OR b OR c"). This mirrors the CLI
// semantics of `slinit-journalctl -u a -u b -p err` = (a OR b) AND
// (priority ≤ err).
type QueryFilter struct {
	// Units is the set of unit names to include. Empty means all.
	Units []string
	// MinPriority is the highest priority value to include (numerically
	// higher is less urgent, so MinPriority=3 keeps emerg..err and drops
	// warn..debug). -1 means no priority filter.
	MinPriority Priority
	// Since is the earliest wall-clock time to include (Unix nanos).
	// Zero means no lower bound.
	Since int64
	// Until is the latest wall-clock time to include (Unix nanos).
	// Zero means no upper bound.
	Until int64
	// Transports is the set of transports to include. Empty means all.
	Transports []Transport

	// Identifiers is the set of SYSLOG_IDENTIFIER values to include
	// (systemd `journalctl -t IDENT`). Empty means all. Match is
	// against Event.SyslogIdentifier with a fallback to Unit / Comm
	// via identOf-equivalent resolution, matching systemd's -t
	// semantics of "match the identifier the operator sees in short
	// output".
	Identifiers []string
	// ExcludeIdentifiers is the systemd `-T IDENT` inverse of
	// Identifiers — any event whose resolved identifier is in this
	// set is dropped. Applied after Identifiers include-filter.
	ExcludeIdentifiers []string
	// GrepPattern, if non-empty, is an RE2-compatible regex the event
	// Msg must match (systemd `-g PATTERN`). Compiled at filter build
	// time, so callers pay the compile cost once per query.
	GrepPattern string
	// GrepInsensitive folds ASCII case in GrepPattern (systemd
	// `--case-sensitive=no` / `-g` default heuristic).
	GrepInsensitive bool
}

// hasMinPriority is a sentinel test that distinguishes "priority
// filter disabled" from "keep only PriorityEmergency" (both look like
// 0 in the zero-value case). We use MinPriority < 0 as the disabled
// signal so the caller can zero the QueryFilter struct and get an
// unfiltered query.
func (q QueryFilter) hasMinPriority() bool { return q.MinPriority >= 0 }

// Match returns true if the event passes every dimension of the
// filter. Called by Query per candidate; a nil filter accepts
// everything, which lets slinit-journalctl short-circuit the
// slow path when no filters were passed.
func (q QueryFilter) Match(e *Event) bool {
	if len(q.Units) > 0 {
		ok := false
		for _, u := range q.Units {
			if e.Unit == u {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if q.hasMinPriority() && e.Prio > q.MinPriority {
		return false
	}
	if q.Since > 0 && e.Ts < q.Since {
		return false
	}
	if q.Until > 0 && e.Ts > q.Until {
		return false
	}
	if len(q.Transports) > 0 {
		ok := false
		for _, t := range q.Transports {
			if e.Transport == t {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(q.Identifiers) > 0 {
		ident := ResolveIdentifier(e)
		ok := false
		for _, id := range q.Identifiers {
			if ident == id {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(q.ExcludeIdentifiers) > 0 {
		ident := ResolveIdentifier(e)
		for _, id := range q.ExcludeIdentifiers {
			if ident == id {
				return false
			}
		}
	}
	if q.GrepPattern != "" && !grepMatch(q.GrepPattern, q.GrepInsensitive, e.Msg) {
		return false
	}
	return true
}

// ResolveIdentifier picks the display identifier for an event using
// the same fallback chain as short-format renderers: SyslogIdentifier
// wins, then Unit, then Comm, then "unknown". Exported so filters and
// renderers share one source of truth for what "identifier" means.
func ResolveIdentifier(e *Event) string {
	switch {
	case e.SyslogIdentifier != "":
		return e.SyslogIdentifier
	case e.Unit != "":
		return e.Unit
	case e.Comm != "":
		return e.Comm
	default:
		return "unknown"
	}
}

// grepMatch reports whether pattern matches s. Compilation errors
// return false (drop the event) — pattern should have been validated
// at flag-parse time, so a runtime miss means either an empty pattern
// or an unrecoverable programmer error. Regex compiled per call because
// QueryFilter is value-typed and short-lived; the caller runs at most
// one Match loop over the buffer per query.
func grepMatch(pattern string, insensitive bool, s string) bool {
	if insensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(s)
}

// isEmpty reports whether the filter is fully disabled — every
// dimension is zero-value. Used by Query to short-circuit the per-
// event Match loop when no filters are set. QueryFilter contains a
// slice field so we can't compare with ==; the check is per-field
// but still cheap.
func (q QueryFilter) isEmpty() bool {
	return len(q.Units) == 0 &&
		!q.hasMinPriority() &&
		q.Since == 0 && q.Until == 0 &&
		len(q.Transports) == 0 &&
		len(q.Identifiers) == 0 &&
		len(q.ExcludeIdentifiers) == 0 &&
		q.GrepPattern == ""
}

// Query returns events from the buffer matching filter, in chronological
// order. limit ≤ 0 returns all matches; limit > 0 returns at most limit
// (most recent kept). Also returns the current Seq so the caller can
// detect eviction between calls.
func (b *EventBuffer) Query(filter QueryFilter, limit int) (events []*Event, seq uint64) {
	snap, s := b.Snapshot()

	if filter.isEmpty() {
		// Fast path: no filter set. Return whole snapshot (or its tail).
		if limit > 0 && len(snap) > limit {
			snap = snap[len(snap)-limit:]
		}
		return snap, s
	}

	// Slow path: filter each event.
	matched := make([]*Event, 0, len(snap))
	for _, e := range snap {
		if filter.Match(e) {
			matched = append(matched, e)
		}
	}
	if limit > 0 && len(matched) > limit {
		matched = matched[len(matched)-limit:]
	}
	return matched, s
}

// PruneOlderThan removes events with Ts older than the given
// wall-clock time. Called by the periodic maintenance loop to keep
// stale entries from lingering — useful when a mostly-idle system
// has entries days old that are no longer relevant. Not required for
// correctness (the ring already bounds memory via capacity), but
// improves query relevance for `slinit-journalctl -n 100` on
// long-uptime systems.
func (b *EventBuffer) PruneOlderThan(cutoff time.Time) int {
	cutoffNs := cutoff.UnixNano()

	b.mu.Lock()
	defer b.mu.Unlock()

	// Take a chronologically-ordered snapshot inline (to avoid the
	// per-call allocation of the public Snapshot method), filter, and
	// rebuild the ring from the survivors.
	live := make([]*Event, 0, b.size)
	if b.size < b.capacity {
		for i := 0; i < b.size; i++ {
			if b.events[i].Ts >= cutoffNs {
				live = append(live, b.events[i])
			}
		}
	} else {
		for i := 0; i < b.capacity; i++ {
			idx := (b.head + i) % b.capacity
			if b.events[idx].Ts >= cutoffNs {
				live = append(live, b.events[idx])
			}
		}
	}
	pruned := b.size - len(live)

	// Rewrite from a clean slate.
	for i := range b.events {
		b.events[i] = nil
	}
	for i, e := range live {
		b.events[i] = e
	}
	b.head = len(live) % b.capacity
	b.size = len(live)
	return pruned
}
