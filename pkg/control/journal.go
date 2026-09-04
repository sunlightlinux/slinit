package control

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// JournalQueryRequest is the wire form of a CmdJournalQuery payload.
// JSON was chosen over a hand-rolled binary layout because the payload
// is tiny (~200 B typical), fits under MaxPayloadSize by a large margin,
// and stays trivially evolvable — new filter dimensions land as
// additional keys without a protocol version bump. Wire compatibility
// with slinit-journalctl is what pins the JSON tag names, so treat this
// struct as append-only.
type JournalQueryRequest struct {
	// Units is the OR-set of unit names to match. Empty means all units.
	Units []string `json:"units,omitempty"`

	// MinPriority is the highest priority value kept (numerically higher
	// is less urgent). Zero + PrioritySet=false means "no filter";
	// PrioritySet=true is the sentinel that distinguishes "MinPriority=0
	// = keep only emerg" from the default.
	MinPriority  int  `json:"min_priority,omitempty"`
	PrioritySet  bool `json:"prio_set,omitempty"`

	// Since / Until bound the wall-clock time range (Unix nanoseconds).
	// Zero means unbounded on that side.
	Since int64 `json:"since,omitempty"`
	Until int64 `json:"until,omitempty"`

	// Transports is the OR-set of transport tags to keep. Empty = all.
	// Values match journal.Transport constants ("driver", "stdout",
	// "kernel", "native", "syslog").
	Transports []string `json:"transports,omitempty"`

	// Identifiers is the OR-set of SYSLOG_IDENTIFIER values to keep
	// (systemd `-t IDENT`). Match resolves against Event's SyslogID
	// with Unit/Comm fallback per journal.ResolveIdentifier.
	Identifiers []string `json:"identifiers,omitempty"`
	// ExcludeIdentifiers drops events whose resolved identifier is in
	// this set (systemd `-T IDENT`). Applied after Identifiers.
	ExcludeIdentifiers []string `json:"exclude_identifiers,omitempty"`
	// GrepPattern is an RE2 regex the event message must match
	// (systemd `-g PATTERN`). Empty means no grep filter.
	GrepPattern string `json:"grep,omitempty"`
	// GrepInsensitive folds ASCII case for GrepPattern.
	GrepInsensitive bool `json:"grep_i,omitempty"`
	// InvocationID filters events by their SLINIT_INVOCATION_ID
	// field (systemd `--invocation=UUID`). Empty means no filter.
	InvocationID string `json:"invocation_id,omitempty"`
	// Namespace filters events by their Event.Namespace tag
	// (systemd `--namespace=NS`). Empty = no filter.
	Namespace string `json:"namespace,omitempty"`
	BootID    string `json:"boot_id,omitempty"`

	// Limit caps the number of matching events returned. Zero or
	// negative means "no cap"; positive limits keep the most recent N.
	Limit int `json:"limit,omitempty"`
}

// ToFilter converts the wire request into the journal package's own
// QueryFilter type. The two are kept separate so pkg/control isn't
// pinned to internal journal struct shapes. Exported so slinit-journalctl
// can reuse the same conversion when reading a JSONL file directly
// (--file bypasses the socket path but must apply identical filter
// semantics for bit-identical output).
func (req JournalQueryRequest) ToFilter() journal.QueryFilter {
	f := journal.QueryFilter{
		Units:              req.Units,
		Since:              req.Since,
		Until:              req.Until,
		Identifiers:        req.Identifiers,
		ExcludeIdentifiers: req.ExcludeIdentifiers,
		GrepPattern:        req.GrepPattern,
		GrepInsensitive:    req.GrepInsensitive,
		InvocationID:       req.InvocationID,
		Namespace:          req.Namespace,
		BootID:             req.BootID,
	}
	if req.PrioritySet {
		f.MinPriority = journal.Priority(req.MinPriority)
	} else {
		f.MinPriority = -1 // journal.QueryFilter's "disabled" sentinel
	}
	if len(req.Transports) > 0 {
		f.Transports = make([]journal.Transport, len(req.Transports))
		for i, t := range req.Transports {
			f.Transports[i] = journal.Transport(t)
		}
	}
	return f
}

// handleJournalQuery reads a JSON-encoded JournalQueryRequest, resolves
// it against the process-wide journal ring buffer, and streams matching
// events back as RplyJournalEntry packets followed by RplyJournalDone.
//
// The response pattern mirrors CmdListServices5 (multi-packet stream +
// terminal marker). Each RplyJournalEntry carries the raw JSONL bytes
// produced by Event.MarshalJSONL, so clients need only a JSON parser —
// they don't need to link against pkg/journal to decode.
//
// Errors surface as RplyJournalErr with a UTF-8 diagnostic. The client
// (slinit-journalctl) prints them verbatim.
func (c *Connection) handleJournalQuery(payload []byte) error {
	buf := journal.GlobalBuffer()
	if buf == nil {
		return c.writePacket(RplyJournalErr, []byte("journal: no buffer available (SetGlobal not called)"))
	}

	var req JournalQueryRequest
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &req); err != nil {
			return c.writePacket(RplyJournalErr, []byte(fmt.Sprintf("journal: bad request: %v", err)))
		}
	}

	events, _ := buf.Query(req.ToFilter(), req.Limit)
	for _, evt := range events {
		data, err := evt.MarshalJSONL()
		if err != nil {
			// One oversized/broken event should not sink the whole
			// stream — skip it and keep going. The dropped counter on
			// the emitter side would surface these; slinit-journalctl
			// users just see "one fewer entry".
			continue
		}
		if err := c.writePacket(RplyJournalEntry, data); err != nil {
			return err
		}
	}
	return c.writePacket(RplyJournalDone, nil)
}

// handleJournalSubscribe streams historical events matching the filter
// (like handleJournalQuery does) and then keeps the connection open,
// pushing every subsequent Emit that matches. Terminates when the
// client closes its end of the socket — detected as a write failure
// on the next RplyJournalEntry attempt.
//
// The subscription buffer is sized at 512 slots; a burst of that many
// events between two select iterations gets dropped for slow readers
// rather than blocking the daemon. That matches systemd-journald's
// "we favor liveness over completeness for follow readers" behavior.
func (c *Connection) handleJournalSubscribe(payload []byte) error {
	var req JournalQueryRequest
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &req); err != nil {
			return c.writePacket(RplyJournalErr, []byte(fmt.Sprintf("journal: bad request: %v", err)))
		}
	}
	filter := req.ToFilter()

	// 1. Backlog first — matches user expectation for `slinit-journalctl -f`
	// (systemd prints last N then follows). Only if a buffer exists.
	if buf := journal.GlobalBuffer(); buf != nil {
		past, _ := buf.Query(filter, req.Limit)
		for _, evt := range past {
			data, err := evt.MarshalJSONL()
			if err != nil {
				continue
			}
			if err := c.writePacket(RplyJournalEntry, data); err != nil {
				return err
			}
		}
	}

	// 2. Subscribe to new events. Nil channel means no global emitter
	// (test / embedded mode) — treat as "nothing to follow", close cleanly.
	ch, cancel := journal.GlobalSubscribe(512)
	if ch == nil {
		return c.writePacket(RplyJournalErr, []byte("journal: no emitter available"))
	}
	defer cancel()

	// Watcher goroutine: a follow session is one-way (client sends
	// nothing after the initial CmdJournalSubscribe payload), so we
	// can use a blocking read to detect connection close. When the
	// client hangs up (or the server calls Stop), io.Copy returns
	// and closes done — the main select breaks the fan-out loop so
	// this handler doesn't leak past its connection.
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, c.conn)
		close(done)
	}()

	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return nil
			}
			if !filter.Match(evt) {
				continue
			}
			data, err := evt.MarshalJSONL()
			if err != nil {
				continue
			}
			if err := c.writePacket(RplyJournalEntry, data); err != nil {
				// Client closed. Return nil (not err) — a follow session
				// ending is normal, not a protocol failure worth logging.
				return nil
			}
		case <-done:
			return nil
		}
	}
}
