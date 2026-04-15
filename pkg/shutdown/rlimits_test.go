package shutdown

import (
	"errors"
	"syscall"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/logging"
)

func TestParseBootRlimits_Basic(t *testing.T) {
	got, err := ParseBootRlimits("nofile=65536")
	if err != nil {
		t.Fatalf("ParseBootRlimits: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 limit, got %d", len(got))
	}
	if got[0].Resource != syscall.RLIMIT_NOFILE {
		t.Errorf("resource = %d, want %d", got[0].Resource, syscall.RLIMIT_NOFILE)
	}
	if got[0].Soft != 65536 || got[0].Hard != 65536 {
		t.Errorf("soft/hard = %d/%d, want 65536/65536", got[0].Soft, got[0].Hard)
	}
}

func TestParseBootRlimits_SoftHardPair(t *testing.T) {
	got, err := ParseBootRlimits("stack=8388608:unlimited")
	if err != nil {
		t.Fatalf("ParseBootRlimits: %v", err)
	}
	if got[0].Soft != 8388608 {
		t.Errorf("soft = %d, want 8388608", got[0].Soft)
	}
	if got[0].Hard != RlimUnlimited {
		t.Errorf("hard = %x, want RlimUnlimited", got[0].Hard)
	}
}

func TestParseBootRlimits_Unlimited(t *testing.T) {
	got, err := ParseBootRlimits("core=unlimited")
	if err != nil {
		t.Fatalf("ParseBootRlimits: %v", err)
	}
	if got[0].Soft != RlimUnlimited || got[0].Hard != RlimUnlimited {
		t.Errorf("unlimited not set: %+v", got[0])
	}
}

func TestParseBootRlimits_MultipleNames(t *testing.T) {
	got, err := ParseBootRlimits("nofile=4096, core=0, stack=1048576:2097152, nproc=8192")
	if err != nil {
		t.Fatalf("ParseBootRlimits: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("want 4 entries, got %d", len(got))
	}

	// Build a map for assertion order-independence.
	byRes := map[int]BootRlimit{}
	for _, l := range got {
		byRes[l.Resource] = l
	}

	if v := byRes[syscall.RLIMIT_NOFILE]; v.Soft != 4096 || v.Hard != 4096 {
		t.Errorf("nofile = %d:%d, want 4096:4096", v.Soft, v.Hard)
	}
	if v := byRes[syscall.RLIMIT_CORE]; v.Soft != 0 || v.Hard != 0 {
		t.Errorf("core = %d:%d, want 0:0", v.Soft, v.Hard)
	}
	if v := byRes[syscall.RLIMIT_STACK]; v.Soft != 1048576 || v.Hard != 2097152 {
		t.Errorf("stack = %d:%d, want 1048576:2097152", v.Soft, v.Hard)
	}
	if v := byRes[rlimitNPROC]; v.Soft != 8192 || v.Hard != 8192 {
		t.Errorf("nproc = %d:%d, want 8192:8192", v.Soft, v.Hard)
	}
}

func TestParseBootRlimits_StripPrefix(t *testing.T) {
	got, err := ParseBootRlimits("rlimit-nofile=1024,RLIMIT_CORE=0")
	if err != nil {
		t.Fatalf("ParseBootRlimits: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got))
	}
}

func TestParseBootRlimits_Empty(t *testing.T) {
	got, err := ParseBootRlimits("")
	if err != nil {
		t.Fatalf("empty: %v", err)
	}
	if got != nil {
		t.Errorf("want nil, got %v", got)
	}

	got, err = ParseBootRlimits("   ")
	if err != nil {
		t.Fatalf("whitespace: %v", err)
	}
	if got != nil {
		t.Errorf("want nil, got %v", got)
	}
}

func TestParseBootRlimits_Errors(t *testing.T) {
	cases := []string{
		"nofile",               // missing =
		"unknown=123",          // unknown name
		"nofile=",              // empty value
		"nofile=abc",           // not a number
		"nofile=1024:abc",      // bad hard
		"nofile=1024, nofile=2048", // duplicate
		"nofile=2048:1024",     // soft > hard
	}
	for _, in := range cases {
		if _, err := ParseBootRlimits(in); err == nil {
			t.Errorf("ParseBootRlimits(%q) should have failed", in)
		}
	}
}

func TestParseRlimitValue_Infinity(t *testing.T) {
	s, h, err := parseRlimitValue("infinity")
	if err != nil {
		t.Fatalf("infinity: %v", err)
	}
	if s != RlimUnlimited || h != RlimUnlimited {
		t.Errorf("infinity not recognised: %d/%d", s, h)
	}
}

func TestApplyBootRlimits_CallsSetrlimit(t *testing.T) {
	orig := setrlimitFunc
	t.Cleanup(func() { setrlimitFunc = orig })

	type call struct {
		resource int
		cur, max uint64
	}
	var calls []call
	setrlimitFunc = func(resource int, rlim *syscall.Rlimit) error {
		calls = append(calls, call{resource, rlim.Cur, rlim.Max})
		return nil
	}

	limits := []BootRlimit{
		{Name: "nofile", Resource: syscall.RLIMIT_NOFILE, Soft: 4096, Hard: 8192},
		{Name: "core", Resource: syscall.RLIMIT_CORE, Soft: 0, Hard: 0},
	}
	n := ApplyBootRlimits(limits, logging.New(logging.LevelDebug))
	if n != 2 {
		t.Errorf("applied = %d, want 2", n)
	}
	if len(calls) != 2 {
		t.Fatalf("setrlimit calls = %d, want 2", len(calls))
	}
	if calls[0].resource != syscall.RLIMIT_NOFILE || calls[0].cur != 4096 || calls[0].max != 8192 {
		t.Errorf("nofile call = %+v", calls[0])
	}
	if calls[1].resource != syscall.RLIMIT_CORE || calls[1].cur != 0 || calls[1].max != 0 {
		t.Errorf("core call = %+v", calls[1])
	}
}

func TestApplyBootRlimits_SkipsFailures(t *testing.T) {
	orig := setrlimitFunc
	t.Cleanup(func() { setrlimitFunc = orig })

	callCount := 0
	setrlimitFunc = func(resource int, rlim *syscall.Rlimit) error {
		callCount++
		// Fail on the first call only.
		if callCount == 1 {
			return errors.New("synthetic failure")
		}
		return nil
	}

	limits := []BootRlimit{
		{Name: "nofile", Resource: syscall.RLIMIT_NOFILE, Soft: 4096, Hard: 4096},
		{Name: "core", Resource: syscall.RLIMIT_CORE, Soft: 0, Hard: 0},
	}
	n := ApplyBootRlimits(limits, logging.New(logging.LevelDebug))
	if n != 1 {
		t.Errorf("applied = %d, want 1 (one failure skipped)", n)
	}
	if callCount != 2 {
		t.Errorf("callCount = %d, want 2 (both attempted)", callCount)
	}
}

func TestApplyBootRlimits_NilLoggerSafe(t *testing.T) {
	orig := setrlimitFunc
	t.Cleanup(func() { setrlimitFunc = orig })

	setrlimitFunc = func(resource int, rlim *syscall.Rlimit) error { return nil }

	limits := []BootRlimit{{Name: "nofile", Resource: syscall.RLIMIT_NOFILE, Soft: 1024, Hard: 1024}}
	// Should not panic.
	ApplyBootRlimits(limits, nil)
}

func TestFormatRlimValue(t *testing.T) {
	if got := formatRlimValue(1024); got != "1024" {
		t.Errorf("1024 → %q", got)
	}
	if got := formatRlimValue(RlimUnlimited); got != "unlimited" {
		t.Errorf("RlimUnlimited → %q", got)
	}
}
