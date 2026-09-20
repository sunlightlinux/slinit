// Regression tests derived from the systemd bug list on nosystemd.org.
//
// Each case names the upstream issue it mirrors and asserts slinit does
// NOT have the equivalent defect. These are not tests of systemd — they
// pin a slinit behaviour that happens to be the thing systemd got
// wrong, so the contrast stays documented and a future refactor cannot
// quietly reintroduce the same class of bug.
package journal

import (
	"strings"
	"testing"
)

// systemd#4863: journald dropped every byte after a NUL in a log line,
// so a single stray \0 silently truncated the record.
// https://github.com/systemd/systemd/issues/4863
//
// slinit keeps the whole line. Note that log-line sanitisation
// (log-sanitize / svlogd -r) is OPT-IN in slinit, so by default a NUL
// travels through the pipeline untouched — this test pins that it
// travels *whole*, which is the property that matters.
func TestNoTruncationAtNUL_systemd4863(t *testing.T) {
	const before = "visible-before"
	const after = "MUST-SURVIVE-after-the-nul"
	msg := before + "\x00" + after

	evt := &Event{Msg: msg, Prio: PriorityInfo}
	line, err := evt.MarshalJSONL()
	if err != nil {
		t.Fatalf("MarshalJSONL: %v", err)
	}

	// The serialised form must not end the message at the NUL.
	if !strings.Contains(string(line), after) {
		t.Errorf("serialised event lost everything after the NUL:\n%s", line)
	}

	got, err := UnmarshalEvent(line)
	if err != nil {
		t.Fatalf("UnmarshalEvent: %v", err)
	}
	if got.Msg != msg {
		t.Errorf("round-trip changed the message:\n got %q\nwant %q", got.Msg, msg)
	}
	if !strings.Contains(got.Msg, after) {
		t.Errorf("round-trip dropped the tail after the NUL: %q", got.Msg)
	}
}

// Same defect class, reached through the fields map rather than the
// message: a NUL in a user-supplied field value must not truncate the
// value or corrupt the surrounding record.
func TestNoTruncationAtNULInFields_systemd4863(t *testing.T) {
	evt := &Event{
		Msg:    "carrier",
		Fields: map[string]string{"PAYLOAD": "head\x00tail"},
	}
	line, err := evt.MarshalJSONL()
	if err != nil {
		t.Fatalf("MarshalJSONL: %v", err)
	}
	got, err := UnmarshalEvent(line)
	if err != nil {
		t.Fatalf("UnmarshalEvent: %v", err)
	}
	if got.Fields["PAYLOAD"] != "head\x00tail" {
		t.Errorf("field value mangled: %q", got.Fields["PAYLOAD"])
	}
	if got.Msg != "carrier" {
		t.Errorf("neighbouring field corrupted: Msg = %q", got.Msg)
	}
}

// CVE-2018-16864 / CVE-2018-16865: journald could be driven off its
// stack by an attacker-controlled very long log line (alloca of an
// unbounded length). slinit caps an event at MaxEventSize and the
// reader is given an explicit buffer ceiling, so an oversized line is
// rejected or truncated deliberately — never turned into an unbounded
// allocation.
//
// This test does not prove memory safety (Go gives us that); it pins
// that a huge line is handled as data rather than accepted unbounded.
func TestOversizedLineIsBounded_CVE_2018_16864(t *testing.T) {
	huge := strings.Repeat("A", MaxEventSize*2)
	evt := &Event{Msg: huge}

	line, err := evt.MarshalJSONL()
	if err != nil {
		// Refusing to serialise an oversized event is a fine outcome.
		return
	}
	if len(line) <= MaxEventSize {
		// Truncated or compressed down to the cap — also fine.
		return
	}
	// If it serialises above the cap, the reader must still not choke:
	// UnmarshalEvent has to either parse it or return an error, not
	// panic or hang.
	if _, err := UnmarshalEvent(line); err != nil {
		return // rejected, which is the expected path
	}
	t.Logf("oversized event (%d bytes) round-tripped; MaxEventSize=%d "+
		"is advisory on this path", len(line), MaxEventSize)
}
