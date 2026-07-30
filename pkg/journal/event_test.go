package journal

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPriorityString(t *testing.T) {
	cases := []struct {
		p    Priority
		want string
	}{
		{PriorityEmergency, "emerg"},
		{PriorityError, "err"},
		{PriorityInfo, "info"},
		{PriorityDebug, "debug"},
		{Priority(42), "prio(42)"}, // out of range still stringifies
		{Priority(-1), "prio(-1)"},
	}
	for _, c := range cases {
		if got := c.p.String(); got != c.want {
			t.Errorf("Priority(%d).String() = %q, want %q", int(c.p), got, c.want)
		}
	}
}

func TestPriorityValid(t *testing.T) {
	for p := Priority(-2); p <= Priority(10); p++ {
		got := p.Valid()
		want := p >= 0 && p <= 7
		if got != want {
			t.Errorf("Priority(%d).Valid() = %v, want %v", int(p), got, want)
		}
	}
}

func TestIsValidFieldName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"MESSAGE", true},
		{"PRIORITY", true},
		{"MY_APP_ID", true},
		{"X1", true},
		{"", false},           // empty
		{"_TRANSPORT", false}, // underscore prefix reserved
		{"_PID", false},
		{"a", false},                                   // lowercase
		{"Message", false},                             // mixed case
		{"MESSAGE!", false},                            // punctuation
		{"1FIRST", false},                              // starts with digit
		{strings.Repeat("A", MaxFieldNameLen+1), false}, // too long
		{strings.Repeat("A", MaxFieldNameLen), true},   // max length OK
	}
	for _, c := range cases {
		if got := IsValidFieldName(c.name); got != c.want {
			t.Errorf("IsValidFieldName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestEventValidate(t *testing.T) {
	// Valid baseline.
	e := &Event{
		Ts:   1,
		Prio: PriorityInfo,
		Msg:  "hello",
	}
	if err := e.Validate(); err != nil {
		t.Fatalf("baseline event should validate: %v", err)
	}

	// Missing timestamp — Ts=0 means Now was never called.
	bad := &Event{Prio: PriorityInfo, Msg: "hello"}
	if err := bad.Validate(); err == nil {
		t.Errorf("event without ts should fail validation")
	}

	// Priority out of range.
	bad = &Event{Ts: 1, Prio: Priority(99)}
	if err := bad.Validate(); err == nil {
		t.Errorf("event with invalid priority should fail validation")
	}

	// Field with underscore prefix — should fail.
	bad = &Event{Ts: 1, Prio: PriorityInfo, Fields: map[string]string{"_SPOOF": "x"}}
	if err := bad.Validate(); err == nil {
		t.Errorf("event with underscore-prefixed custom field should fail validation")
	}

	// Field with lowercase — should fail.
	bad = &Event{Ts: 1, Prio: PriorityInfo, Fields: map[string]string{"lowercase": "x"}}
	if err := bad.Validate(); err == nil {
		t.Errorf("event with lowercase custom field should fail validation")
	}
}

func TestClientAssertUntrusted(t *testing.T) {
	// Fully-untrusted event passes.
	clean := &Event{
		Ts:   1,
		Prio: PriorityInfo,
		Msg:  "hello",
		Unit: "nginx",
		Fields: map[string]string{
			"APP_VERSION": "1.0",
		},
	}
	if err := clean.ClientAssertUntrusted(); err != nil {
		t.Errorf("clean event should pass ClientAssertUntrusted: %v", err)
	}

	// Any trusted field set → reject.
	trusted := []func(*Event){
		func(e *Event) { e.Transport = TransportNative },
		func(e *Event) { e.Pid = 1 },
		func(e *Event) { e.Uid = 1000 },
		func(e *Event) { e.BootID = "abc" },
		func(e *Event) { e.MachineID = "def" },
		func(e *Event) { e.Hostname = "sunlight" },
		func(e *Event) { e.Comm = "foo" },
		func(e *Event) { e.Exe = "/bin/foo" },
	}
	for i, set := range trusted {
		e := &Event{Ts: 1, Msg: "x"}
		set(e)
		if err := e.ClientAssertUntrusted(); !errors.Is(err, ErrTrustedFieldSet) {
			t.Errorf("case %d: expected ErrTrustedFieldSet, got %v", i, err)
		}
	}

	// Underscore-prefixed key in Fields — reject.
	sneaky := &Event{
		Ts:     1,
		Msg:    "x",
		Fields: map[string]string{"_pid": "1"}, // lowercase but still underscore
	}
	if err := sneaky.ClientAssertUntrusted(); !errors.Is(err, ErrTrustedFieldSet) {
		t.Errorf("event with underscore-prefixed field should be rejected: %v", err)
	}
}

func TestStripTrusted(t *testing.T) {
	e := &Event{
		Ts:        1,
		Msg:       "x",
		Transport: TransportNative,
		Pid:       1234,
		Uid:       0,
		Gid:       0,
		Comm:      "foo",
		Exe:       "/bin/foo",
		Cmdline:   "foo --arg",
		BootID:    "abc",
		MachineID: "def",
		Hostname:  "sunlight",
	}
	e.StripTrusted()

	if e.Transport != "" || e.Pid != 0 || e.Comm != "" ||
		e.Exe != "" || e.BootID != "" || e.Hostname != "" {
		t.Errorf("StripTrusted should zero all daemon-injected fields, got %+v", e)
	}

	// Client-writable fields must survive.
	if e.Ts != 1 || e.Msg != "x" {
		t.Errorf("StripTrusted must not touch client fields, got %+v", e)
	}
}

func TestNow(t *testing.T) {
	e := &Event{}
	e.Now()
	if e.Ts == 0 {
		t.Errorf("Now should populate Ts")
	}
	if e.Mts == 0 {
		t.Errorf("Now should populate Mts")
	}
	// Sanity: monotonic and wall-clock nanos should both be non-negative
	// and roughly today (post-2020 = 1577836800 * 1e9 ns).
	if e.Ts < 1577836800*1e9 {
		t.Errorf("Ts=%d looks bogus (too far in past)", e.Ts)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	orig := &Event{
		Ts:   1735223456123456789,
		Mts:  1234567890,
		Msg:  "listening on :80",
		Prio: PriorityInfo,
		Unit: "nginx",
		Fields: map[string]string{
			"HTTP_STATUS": "200",
			"REQUEST":     "GET /",
		},
		Transport: TransportStdout,
		Pid:       1234,
		BootID:    "abc123",
	}

	data, err := orig.MarshalJSONL()
	if err != nil {
		t.Fatalf("MarshalJSONL: %v", err)
	}
	// Must be a single line (no trailing newline).
	if strings.Contains(string(data), "\n") {
		t.Errorf("MarshalJSONL should not include newline: %q", data)
	}
	// Must parse as valid JSON.
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("marshaled event is not valid JSON: %v", err)
	}

	got, err := UnmarshalEvent(data)
	if err != nil {
		t.Fatalf("UnmarshalEvent: %v", err)
	}

	if got.Ts != orig.Ts || got.Msg != orig.Msg || got.Unit != orig.Unit {
		t.Errorf("round-trip lost core fields: got %+v want %+v", got, orig)
	}
	if got.Pid != orig.Pid || got.BootID != orig.BootID {
		t.Errorf("round-trip lost trusted fields: got Pid=%d BootID=%q, want %d/%q",
			got.Pid, got.BootID, orig.Pid, orig.BootID)
	}
	if len(got.Fields) != len(orig.Fields) || got.Fields["HTTP_STATUS"] != "200" {
		t.Errorf("round-trip lost Fields: got %+v", got.Fields)
	}
}

func TestMarshalRejectsTooLarge(t *testing.T) {
	// Build a message big enough to blow MaxEventSize.
	e := &Event{
		Ts:  1,
		Msg: strings.Repeat("A", MaxEventSize),
	}
	_, err := e.MarshalJSONL()
	if !errors.Is(err, ErrEventTooLarge) {
		t.Errorf("expected ErrEventTooLarge, got %v", err)
	}
}

func TestUnmarshalRejectsTooLarge(t *testing.T) {
	big := make([]byte, MaxEventSize+1)
	_, err := UnmarshalEvent(big)
	if !errors.Is(err, ErrEventTooLarge) {
		t.Errorf("expected ErrEventTooLarge, got %v", err)
	}
}

func TestOmitEmpty(t *testing.T) {
	// A minimal event should serialize to a compact JSON without noisy
	// zero-valued fields cluttering the on-disk file.
	e := &Event{
		Ts:   1735223456,
		Msg:  "x",
		Prio: PriorityInfo,
	}
	data, err := e.MarshalJSONL()
	if err != nil {
		t.Fatalf("MarshalJSONL: %v", err)
	}
	got := string(data)
	// Must NOT include zero-valued underscore fields.
	forbidden := []string{`"_pid":0`, `"_uid":0`, `"_boot_id":""`, `"_hostname":""`}
	for _, f := range forbidden {
		if strings.Contains(got, f) {
			t.Errorf("output %q should not include zero-valued %q", got, f)
		}
	}
}
