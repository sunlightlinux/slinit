// Package journal implements slinit's structured log event pipeline.
// Events are emitted as JSON Lines (one JSON object per datagram) over
// a Unix SOCK_DGRAM socket at /run/slinit/events.sock, and are also
// mirrored to an in-process ring buffer that slinit-journalctl can
// query without a running slinit-journald.
//
// The design is inspired by systemd-journald's Native protocol but
// stays true to slinit's dinit-derived philosophy: text logfiles
// continue to work unchanged, the persistent journald daemon is
// optional, and the on-disk format is JSONL rather than a bespoke
// binary layout. A companion .idx file per journal gives fast
// bisect on timestamp for --since / --until queries.
//
// Field name convention mirrors systemd:
//
//   - Underscore-prefixed names (_pid, _uid, _boot_id, ...) are
//     daemon-injected metadata. Clients cannot set them; the emitter
//     rejects any attempt to do so. This preserves the "trusted
//     metadata" guarantee that shell-level users can rely on.
//   - Non-underscore names (msg, prio, unit, ...) are client-writable
//     payload fields.
//   - ts / mts are always daemon-populated but are named without
//     underscore for readability in short output modes.
package journal

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Priority mirrors syslog(3) severity levels. Lower is more urgent.
type Priority int

const (
	PriorityEmergency Priority = 0
	PriorityAlert     Priority = 1
	PriorityCritical  Priority = 2
	PriorityError     Priority = 3
	PriorityWarning   Priority = 4
	PriorityNotice    Priority = 5
	PriorityInfo      Priority = 6
	PriorityDebug     Priority = 7
)

// Valid returns true if p is within the 0..7 syslog range.
func (p Priority) Valid() bool { return p >= 0 && p <= 7 }

// String returns the canonical short name for the priority level, or
// "prio(N)" for out-of-range values so a malformed event still round-
// trips through short output modes without crashing the renderer.
func (p Priority) String() string {
	switch p {
	case PriorityEmergency:
		return "emerg"
	case PriorityAlert:
		return "alert"
	case PriorityCritical:
		return "crit"
	case PriorityError:
		return "err"
	case PriorityWarning:
		return "warn"
	case PriorityNotice:
		return "notice"
	case PriorityInfo:
		return "info"
	case PriorityDebug:
		return "debug"
	default:
		return fmt.Sprintf("prio(%d)", int(p))
	}
}

// Transport identifies where the event entered the pipeline. It maps to
// systemd's _TRANSPORT= field and lets query-side tools understand the
// event provenance (kernel vs userspace vs slinit-internal).
type Transport string

const (
	// TransportDriver is slinit itself emitting state transitions,
	// boot/shutdown milestones, warnings.
	TransportDriver Transport = "driver"
	// TransportStdout is a service's stdout/stderr captured by
	// slinit's log reader and re-emitted as structured events.
	TransportStdout Transport = "stdout"
	// TransportKernel is /dev/kmsg output.
	TransportKernel Transport = "kernel"
	// TransportNative is an external client emitting over the events
	// socket (slinit-native protocol equivalent).
	TransportNative Transport = "native"
	// TransportSyslog is a message received via /dev/log (Phase 2+).
	TransportSyslog Transport = "syslog"
)

// Event is one structured log entry. Field names use short snake_case
// to keep JSON payload compact; underscore prefix marks daemon-injected
// (trusted) metadata that clients cannot set through the public emit
// path.
//
// Timestamps are populated by the emitter, never by clients — this
// preserves the ordering invariant on the receiver side.
type Event struct {
	// Timestamp fields — always daemon-populated.

	// Ts is the wall-clock time in nanoseconds since Unix epoch.
	Ts int64 `json:"ts"`
	// Mts is the monotonic clock in nanoseconds. Survives clock
	// changes; used for boot-time analysis and ordering when Ts
	// is unreliable (e.g. before NTP sync).
	Mts int64 `json:"mts"`

	// Client-writable payload.

	// Msg is the log message text. Required for output-mode "short"
	// and above; empty events are allowed for pure-metadata records
	// but are omitted from most renderers.
	Msg string `json:"msg,omitempty"`

	// Prio is the syslog severity (0..7). Defaults to PriorityInfo
	// when emit sees the zero value and no explicit setter was used.
	// The zero-value ambiguity is why emit.go tracks "priority-was-
	// set" separately.
	Prio Priority `json:"prio,omitempty"`

	// Unit is the slinit service name the event belongs to. Empty for
	// kernel messages or events emitted before any service context
	// exists.
	Unit string `json:"unit,omitempty"`

	// SyslogIdentifier lets a client override the display name in
	// short output mode. Falls back to Unit, then to Comm, then to
	// "unknown". Matches systemd SYSLOG_IDENTIFIER.
	SyslogIdentifier string `json:"syslog_identifier,omitempty"`

	// Fields carries user-defined key/value pairs. Keys must NOT
	// start with underscore (reserved for daemon metadata) and must
	// be validated by IsValidFieldName. Values are stored as strings
	// because the on-disk JSONL format is text; encode structured
	// payloads yourself.
	Fields map[string]string `json:"fields,omitempty"`

	// Daemon-injected trusted metadata. Emit sets these; validation
	// rejects any attempt to set them client-side.

	// Transport is the ingestion path (see Transport constants).
	Transport Transport `json:"_transport,omitempty"`

	// Pid, Uid, Gid come from SO_PASSCRED on the sender's socket.
	Pid int `json:"_pid,omitempty"`
	Uid int `json:"_uid,omitempty"`
	Gid int `json:"_gid,omitempty"`

	// Comm, Exe, Cmdline are captured at emit time by reading
	// /proc/<pid>/. They may become stale for long-running services
	// that rename themselves; for slinit-internal emits (driver
	// transport) they are derived from the record instead.
	Comm    string `json:"_comm,omitempty"`
	Exe     string `json:"_exe,omitempty"`
	Cmdline string `json:"_cmdline,omitempty"`

	// BootID is the current-boot 128-bit hex identifier. Regenerated
	// at each PID-1 start and persisted in /run/slinit/boot-id.
	BootID string `json:"_boot_id,omitempty"`
	// MachineID is the persistent host identity, read from
	// /etc/machine-id (or generated on first boot).
	MachineID string `json:"_machine_id,omitempty"`
	// Hostname is captured at emit time from unix.Uname.Nodename.
	Hostname string `json:"_hostname,omitempty"`
}

// Now sets Ts + Mts to the current time. Called by emit before send,
// never by clients — clients passing pre-populated timestamps risk
// out-of-order journals if system time steps.
func (e *Event) Now() {
	now := time.Now()
	e.Ts = now.UnixNano()
	// Use monotonic component of time.Now if available (Go stamps it
	// on time values that never crossed the JSON boundary). We can't
	// extract it directly, so approximate with unix.ClockGettime.
	e.Mts = monotonicNanos()
}

// Validate returns an error if the event violates the schema:
// underscore-prefixed field keys, invalid priority, empty transport
// after emit, etc. Called by emit before send and by the receiver
// before persisting; malformed events are dropped with a warning so a
// single bad client cannot corrupt the journal.
func (e *Event) Validate() error {
	if !e.Prio.Valid() {
		return fmt.Errorf("journal: invalid priority %d (must be 0..7)", e.Prio)
	}
	if e.Ts == 0 {
		return errors.New("journal: ts is required (call Now before Validate)")
	}
	for k := range e.Fields {
		if !IsValidFieldName(k) {
			return fmt.Errorf("journal: invalid field name %q (must be [A-Z0-9_] and not start with _)", k)
		}
	}
	return nil
}

// IsValidFieldName mirrors systemd's journal_field_valid: names must be
// [A-Z][A-Z0-9_]*, must not start with an underscore (reserved for
// daemon metadata), and must be between 1 and MaxFieldNameLen bytes.
// The underscore-prefix rule is what prevents clients from spoofing
// trusted fields like _PID.
func IsValidFieldName(name string) bool {
	if len(name) == 0 || len(name) > MaxFieldNameLen {
		return false
	}
	if name[0] == '_' {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') && c != '_' {
			return false
		}
	}
	// First char must be a letter.
	c := name[0]
	if !(c >= 'A' && c <= 'Z') {
		return false
	}
	return true
}

// Size limits mirror systemd's ENTRY_SIZE_MAX / DATA_SIZE_MAX /
// ENTRY_FIELD_COUNT_MAX. They cap the damage a misbehaving or
// malicious client can do to the journal (DoS via giant messages or
// tens of thousands of fields per event).
const (
	// MaxMessageLen bounds the msg field. Matches systemd DATA_SIZE_MAX
	// (256 KiB) minus room for structural overhead.
	MaxMessageLen = 256 * 1024
	// MaxEventSize is the total JSON-serialized size cap per event.
	MaxEventSize = 512 * 1024
	// MaxFields is the maximum number of Fields entries per event.
	// Matches systemd ENTRY_FIELD_COUNT_MAX.
	MaxFields = 64
	// MaxFieldValueLen bounds a single value in Fields.
	MaxFieldValueLen = 64 * 1024
	// MaxFieldNameLen bounds a field key length.
	MaxFieldNameLen = 128
)

// MarshalJSONL serializes the event as a single JSON line (no trailing
// newline). Callers that emit to a stream should append "\n" between
// events. Encoded size is checked against MaxEventSize; oversized
// events return ErrEventTooLarge.
func (e *Event) MarshalJSONL() ([]byte, error) {
	buf, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	if len(buf) > MaxEventSize {
		return nil, ErrEventTooLarge
	}
	return buf, nil
}

// UnmarshalEvent parses one JSONL line back into an Event. The caller
// is expected to strip the trailing newline before calling. Underscore
// fields are preserved so receiver-side code can trust them; the
// client-side path validates on the way in, not on the way out.
func UnmarshalEvent(data []byte) (*Event, error) {
	if len(data) > MaxEventSize {
		return nil, ErrEventTooLarge
	}
	var e Event
	// DisallowUnknownFields would break forward compatibility (older
	// slinit reading events written by a newer emitter); use plain
	// decode and drop unknown keys silently.
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("journal: parse event: %w", err)
	}
	return &e, nil
}

// StripTrusted removes daemon-injected fields from the event. Used by
// the emit path before sending to the events socket — the receiver
// re-populates them from SO_PASSCRED / /proc lookups. This guarantees
// clients cannot spoof _pid / _uid / _boot_id even if their in-memory
// Event carried leftover values.
func (e *Event) StripTrusted() {
	e.Transport = ""
	e.Pid = 0
	e.Uid = 0
	e.Gid = 0
	e.Comm = ""
	e.Exe = ""
	e.Cmdline = ""
	e.BootID = ""
	e.MachineID = ""
	e.Hostname = ""
}

// Various errors returned by the package. Kept as sentinels so callers
// can errors.Is() them and drop events cleanly on a size cap without
// misclassifying a network error.
var (
	// ErrEventTooLarge is returned when an event exceeds MaxEventSize
	// on marshal or unmarshal.
	ErrEventTooLarge = errors.New("journal: event too large")
	// ErrTrustedFieldSet is returned when a client tries to set a
	// daemon-only field through the public emit API.
	ErrTrustedFieldSet = errors.New("journal: client set trusted field (name starts with _)")
)

// ClientAssertUntrusted returns an error if any daemon-only field is
// populated. Called by the client-side emit path to enforce the
// underscore-prefix rule at the API boundary, not just at parse time.
func (e *Event) ClientAssertUntrusted() error {
	if e.Transport != "" || e.Pid != 0 || e.Uid != 0 || e.Gid != 0 ||
		e.Comm != "" || e.Exe != "" || e.Cmdline != "" ||
		e.BootID != "" || e.MachineID != "" || e.Hostname != "" {
		return ErrTrustedFieldSet
	}
	for k := range e.Fields {
		if strings.HasPrefix(k, "_") {
			return ErrTrustedFieldSet
		}
	}
	return nil
}
