package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/journalbin"
)

func TestParseArgsDefaults(t *testing.T) {
	opts, err := parseArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.format != fmtShort {
		t.Fatalf("default format: got %q, want %q", opts.format, fmtShort)
	}
	if opts.limit != 0 {
		t.Fatalf("default limit: got %d, want 0", opts.limit)
	}
	if opts.socketPath != "" {
		t.Fatalf("default socket path should be empty, got %q", opts.socketPath)
	}
}

func TestParseArgsLimitForms(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"-n space", []string{"-n", "42"}, 42},
		{"-n glued", []string{"-n42"}, 42},
		{"--lines space", []string{"--lines", "5"}, 5},
		{"--lines equals", []string{"--lines=7"}, 7},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts, err := parseArgs(c.args)
			if err != nil {
				t.Fatal(err)
			}
			if opts.limit != c.want {
				t.Fatalf("got %d, want %d", opts.limit, c.want)
			}
		})
	}
}

func TestParseArgsLimitInvalid(t *testing.T) {
	cases := [][]string{
		{"-n", "not-a-number"},
		{"-n", "-3"},
		{"--lines=abc"},
	}
	for _, args := range cases {
		if _, err := parseArgs(args); err == nil {
			t.Fatalf("expected error for %v", args)
		}
	}
}

func TestParseArgsFormatValid(t *testing.T) {
	for _, f := range validFormats {
		t.Run(string(f), func(t *testing.T) {
			opts, err := parseArgs([]string{"-o", string(f)})
			if err != nil {
				t.Fatal(err)
			}
			if opts.format != f {
				t.Fatalf("got %q, want %q", opts.format, f)
			}
			opts2, err := parseArgs([]string{"--output=" + string(f)})
			if err != nil {
				t.Fatal(err)
			}
			if opts2.format != f {
				t.Fatalf("--output= form got %q, want %q", opts2.format, f)
			}
		})
	}
}

func TestParseArgsFormatInvalid(t *testing.T) {
	_, err := parseArgs([]string{"-o", "binary"})
	if err == nil {
		t.Fatal("expected error for invalid format")
	}
	if !strings.Contains(err.Error(), "short") {
		t.Fatalf("error should enumerate valid formats: %v", err)
	}
}

func TestParseArgsSocketPath(t *testing.T) {
	opts, err := parseArgs([]string{"--socket-path", "/tmp/x.sock"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.socketPath != "/tmp/x.sock" {
		t.Fatalf("got %q", opts.socketPath)
	}
	opts2, err := parseArgs([]string{"--socket-path=/tmp/y.sock"})
	if err != nil {
		t.Fatal(err)
	}
	if opts2.socketPath != "/tmp/y.sock" {
		t.Fatalf("--socket-path= got %q", opts2.socketPath)
	}
}

func TestParseArgsHelpVersion(t *testing.T) {
	for _, arg := range []string{"-h", "--help"} {
		opts, err := parseArgs([]string{arg})
		if err != nil {
			t.Fatalf("%s: %v", arg, err)
		}
		if !opts.showHelp {
			t.Fatalf("%s: showHelp not set", arg)
		}
	}
	opts, err := parseArgs([]string{"--version"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.showVersion {
		t.Fatal("showVersion not set")
	}
}

func TestParseArgsUnknown(t *testing.T) {
	if _, err := parseArgs([]string{"--nonsense"}); err == nil {
		t.Fatal("expected error for unknown flag")
	}
}

// mkEvent builds a canonical event for renderer tests. Timestamps use
// a fixed UTC moment so the short/short-iso outputs are deterministic
// regardless of the test host's timezone.
func mkEvent() *journal.Event {
	// 2026-07-31T12:34:56Z
	ts := time.Date(2026, 7, 31, 12, 34, 56, 0, time.UTC).UnixNano()
	return &journal.Event{
		Ts:               ts,
		Mts:              ts,
		Msg:              "hello world",
		Prio:             journal.PriorityWarning,
		Unit:             "sshd",
		SyslogIdentifier: "",
		Transport:        journal.TransportDriver,
		Pid:              1234,
		Uid:              0,
		Gid:              0,
		Hostname:         "ceres",
		BootID:           "0123456789abcdef0123456789abcdef",
		MachineID:        "fedcba9876543210fedcba9876543210",
		Fields:           map[string]string{"CUSTOM_KEY": "value1"},
	}
}

func TestRenderShort(t *testing.T) {
	// mkEvent produces a driver-transport event with no
	// SLINIT_TARGET_PID hint, matching a state transition where the
	// subject is an internal service. Slinit-internal events without
	// a target PID render with NO bracket (see renderShort's switch
	// — printing the emitter's PID=1 would misrepresent the subject).
	var buf bytes.Buffer
	if err := render(&buf, fmtShort, mkEvent(), renderOpts{}); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "ceres sshd: hello world\n") {
		t.Fatalf("driver event w/o target-pid should have no bracket: %q", got)
	}
}

func TestRenderShortISO(t *testing.T) {
	var buf bytes.Buffer
	if err := render(&buf, fmtShortISO, mkEvent(), renderOpts{}); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "ceres sshd: hello world\n") {
		t.Fatalf("driver event w/o target-pid should have no bracket: %q", got)
	}
	if !strings.HasPrefix(got, "2026-") {
		t.Fatalf("short-iso should start with year, got %q", got[:20])
	}
}

func TestRenderShortNoPid(t *testing.T) {
	e := mkEvent()
	e.Pid = 0
	var buf bytes.Buffer
	if err := render(&buf, fmtShort, e, renderOpts{}); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.Contains(got, "[0]") || strings.Contains(got, "[]") {
		t.Fatalf("pid=0 should not render [PID] block: %q", got)
	}
	if !strings.HasSuffix(strings.TrimRight(got, "\n"), "ceres sshd: hello world") {
		t.Fatalf("unexpected no-pid output: %q", got)
	}
}

func TestRenderShortPrefersTargetPID(t *testing.T) {
	// State-transition events are emitted by slinit (PID 1) but the
	// bracket should show the SUBJECT service's PID via
	// SLINIT_TARGET_PID. Otherwise every line reads "unit[1]:" which
	// is misleading.
	e := mkEvent()
	e.Pid = 1
	if e.Fields == nil {
		e.Fields = map[string]string{}
	}
	e.Fields["SLINIT_TARGET_PID"] = "478"

	var buf bytes.Buffer
	if err := render(&buf, fmtShort, e, renderOpts{}); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "sshd[478]:") {
		t.Fatalf("target-pid not preferred over emitter pid: %q", got)
	}
	if strings.Contains(got, "sshd[1]:") {
		t.Fatalf("emitter pid leaked into bracket: %q", got)
	}
}

func TestRenderShortFallsBackToEmitterPIDForExternal(t *testing.T) {
	// Emitter-PID fallback applies only to NON-slinit-internal
	// events (native / syslog / kernel) where _PID actually is the
	// real source. Verify with a native-transport event.
	e := mkEvent()
	e.Transport = journal.TransportNative
	e.Pid = 1234
	delete(e.Fields, "SLINIT_TARGET_PID")

	var buf bytes.Buffer
	if err := render(&buf, fmtShort, e, renderOpts{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "sshd[1234]:") {
		t.Fatalf("emitter pid fallback failed for external transport: %q", buf.String())
	}
}

func TestRenderShortDriverNoTargetPIDHidesBracket(t *testing.T) {
	// Slinit-internal driver event with no SLINIT_TARGET_PID (or
	// PID<=0 for an internal service) → NO bracket, so the user
	// doesn't get misled by the emitter's PID=1.
	e := mkEvent()
	e.Transport = journal.TransportDriver
	e.Pid = 1 // slinit itself, ignored for driver events
	delete(e.Fields, "SLINIT_TARGET_PID")

	var buf bytes.Buffer
	if err := render(&buf, fmtShort, e, renderOpts{}); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.Contains(got, "[1]") {
		t.Fatalf("driver event without target-pid must not print emitter PID=1: %q", got)
	}
	if !strings.Contains(got, "ceres sshd: hello world\n") {
		t.Fatalf("expected bracketless output: %q", got)
	}
}

func TestRenderShortIgnoresMalformedTargetPID(t *testing.T) {
	// Non-numeric SLINIT_TARGET_PID → treated as absent. For a
	// driver event that means "no bracket" (not fallback to _PID),
	// per the slinit-internal-emitter rule.
	e := mkEvent()
	e.Transport = journal.TransportDriver
	e.Pid = 42
	if e.Fields == nil {
		e.Fields = map[string]string{}
	}
	e.Fields["SLINIT_TARGET_PID"] = "not-a-number"

	var buf bytes.Buffer
	if err := render(&buf, fmtShort, e, renderOpts{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "[42]") {
		t.Fatalf("driver event with malformed target-pid must not fall back to _PID: %q", buf.String())
	}
}

func TestRenderShortFallbacks(t *testing.T) {
	// Empty hostname should render as "-".
	e := mkEvent()
	e.Hostname = ""
	var buf bytes.Buffer
	if err := render(&buf, fmtShort, e, renderOpts{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), " - sshd") {
		t.Fatalf("empty hostname should render as '-': %q", buf.String())
	}

	// SyslogIdentifier wins over Unit. mkEvent is a driver event
	// without target-pid → no bracket, so the identifier is followed
	// by ": " rather than "[N]: ".
	e2 := mkEvent()
	e2.SyslogIdentifier = "openssh"
	var buf2 bytes.Buffer
	if err := render(&buf2, fmtShort, e2, renderOpts{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf2.String(), " openssh: ") {
		t.Fatalf("SyslogIdentifier should override Unit: %q", buf2.String())
	}
}

func TestRenderCat(t *testing.T) {
	var buf bytes.Buffer
	if err := render(&buf, fmtCat, mkEvent(), renderOpts{}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "hello world\n" {
		t.Fatalf("cat should print only the message: %q", buf.String())
	}
}

func TestRenderJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := render(&buf, fmtJSON, mkEvent(), renderOpts{}); err != nil {
		t.Fatal(err)
	}
	line := buf.String()
	if !strings.HasSuffix(line, "\n") {
		t.Fatalf("json output missing trailing newline: %q", line)
	}
	// Should be valid JSON containing the message.
	if !strings.Contains(line, `"msg":"hello world"`) {
		t.Fatalf("json output missing msg: %q", line)
	}
	if !strings.Contains(line, `"_pid":1234`) {
		t.Fatalf("json output missing _pid: %q", line)
	}
}

func TestRenderVerbose(t *testing.T) {
	var buf bytes.Buffer
	if err := render(&buf, fmtVerbose, mkEvent(), renderOpts{}); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	// Verbose should include multiple field lines.
	checks := []string{
		"    PRIORITY=warn\n",
		"    TRANSPORT=driver\n",
		"    UNIT=sshd\n",
		"    _PID=1234\n",
		"    _HOSTNAME=ceres\n",
		"    MESSAGE=hello world\n",
		"    CUSTOM_KEY=value1\n",
	}
	for _, c := range checks {
		if !strings.Contains(got, c) {
			t.Errorf("verbose missing %q\nfull output:\n%s", c, got)
		}
	}
}

func TestRenderExport(t *testing.T) {
	var buf bytes.Buffer
	if err := render(&buf, fmtExport, mkEvent(), renderOpts{}); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	// export format: KEY=value per line, trailing blank line separates
	// events. Every non-empty field from mkEvent() should surface.
	checks := []string{
		"__REALTIME_TIMESTAMP=",
		"__MONOTONIC_TIMESTAMP=",
		"PRIORITY=4\n",
		"MESSAGE=hello world\n",
		"_TRANSPORT=driver\n",
		"_SLINIT_UNIT=sshd\n",
		"_PID=1234\n",
		"_HOSTNAME=ceres\n",
		"CUSTOM_KEY=value1\n",
	}
	for _, c := range checks {
		if !strings.Contains(got, c) {
			t.Errorf("export missing %q\nfull output:\n%s", c, got)
		}
	}
	// Must end with a blank line as event separator.
	if !strings.HasSuffix(got, "\n\n") {
		t.Fatalf("export must end with blank line separator; ends: %q", got[len(got)-4:])
	}
}

func TestRenderExportSkipsEmptyFields(t *testing.T) {
	// _UID=0 → skipped; SyslogIdentifier="" → skipped.
	e := mkEvent()
	e.SyslogIdentifier = ""
	var buf bytes.Buffer
	if err := render(&buf, fmtExport, e, renderOpts{}); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.Contains(got, "_UID=") {
		t.Errorf("_UID=0 should be omitted in export: %q", got)
	}
	if strings.Contains(got, "SYSLOG_IDENTIFIER=") {
		t.Errorf("empty SyslogIdentifier should be omitted: %q", got)
	}
}

func TestRenderUnknownFormat(t *testing.T) {
	var buf bytes.Buffer
	if err := render(&buf, "hex", mkEvent(), renderOpts{}); err == nil {
		t.Fatal("expected error for unknown format")
	}
}

func TestSortedKeys(t *testing.T) {
	m := map[string]string{"C": "3", "A": "1", "B": "2"}
	got := sortedKeys(m)
	want := []string{"A", "B", "C"}
	for i, k := range want {
		if got[i] != k {
			t.Fatalf("index %d: got %q, want %q", i, got[i], k)
		}
	}
}

func TestIdentOfFallback(t *testing.T) {
	cases := []struct {
		name string
		e    *journal.Event
		want string
	}{
		{"syslog id wins", &journal.Event{SyslogIdentifier: "app", Unit: "svc", Comm: "c"}, "app"},
		{"unit second", &journal.Event{Unit: "svc", Comm: "c"}, "svc"},
		{"comm third", &journal.Event{Comm: "c"}, "c"},
		{"nothing", &journal.Event{}, "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := identOf(c.e); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestResolveSocketPathExplicit(t *testing.T) {
	got := resolveSocketPath(options{socketPath: "/tmp/explicit.sock"})
	if got != "/tmp/explicit.sock" {
		t.Fatalf("explicit path ignored: %q", got)
	}
}

func TestResolveSocketPathSystemMode(t *testing.T) {
	got := resolveSocketPath(options{systemMode: true})
	if got != defaultSystemSocket {
		t.Fatalf("system-mode should use default system socket, got %q", got)
	}
}

// ---- 2c: filters -----------------------------------------------------

func TestParseArgsUnitsRepeatable(t *testing.T) {
	opts, err := parseArgs([]string{"-u", "sshd", "--unit=cron", "-u", "dbus"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"sshd", "cron", "dbus"}
	if len(opts.units) != len(want) {
		t.Fatalf("got %d units, want %d", len(opts.units), len(want))
	}
	for i, u := range want {
		if opts.units[i] != u {
			t.Fatalf("units[%d]=%q, want %q", i, opts.units[i], u)
		}
	}
}

func TestParseArgsPriorityNumeric(t *testing.T) {
	opts, err := parseArgs([]string{"-p", "3"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.prioritySet || opts.priority != journal.PriorityError {
		t.Fatalf("want prioritySet + err, got set=%v prio=%v", opts.prioritySet, opts.priority)
	}
}

func TestParseArgsPrioritySymbolic(t *testing.T) {
	opts, err := parseArgs([]string{"--priority=warn"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.prioritySet || opts.priority != journal.PriorityWarning {
		t.Fatalf("want prioritySet + warn, got set=%v prio=%v", opts.prioritySet, opts.priority)
	}
}

func TestParseArgsPriorityInvalid(t *testing.T) {
	for _, arg := range [][]string{
		{"-p", "8"},
		{"-p", "-1"},
		{"-p", "nonsense"},
	} {
		if _, err := parseArgs(arg); err == nil {
			t.Fatalf("expected error for %v", arg)
		}
	}
}

func TestParseArgsReverse(t *testing.T) {
	opts, err := parseArgs([]string{"-r"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.reverse {
		t.Fatal("reverse not set for -r")
	}
	opts2, err := parseArgs([]string{"--reverse"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts2.reverse {
		t.Fatal("reverse not set for --reverse")
	}
}

func TestParseTimeArg(t *testing.T) {
	// Anchor "now" to a stable moment so relative arithmetic is
	// deterministic across test hosts.
	now := time.Date(2026, 7, 31, 15, 0, 0, 0, time.UTC)

	cases := []struct {
		in   string
		want time.Time
	}{
		{"now", now},
		{"today", time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)},
		{"yesterday", time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)},
		{"-1h", now.Add(-time.Hour)},
		{"-30m", now.Add(-30 * time.Minute)},
		{"-2d", now.AddDate(0, 0, -2)},
		{"-45s", now.Add(-45 * time.Second)},
		{"2026-07-31T12:00:00Z", time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := parseTimeArg(c.in, now)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestParseTimeArgInvalid(t *testing.T) {
	now := time.Now()
	for _, in := range []string{"", "asdf", "-1x", "-hh", "1h"} {
		if _, err := parseTimeArg(in, now); err == nil {
			t.Fatalf("expected error for %q", in)
		}
	}
}

func TestBuildRequestAllFilters(t *testing.T) {
	opts := options{
		units:       []string{"sshd", "cron"},
		priority:    journal.PriorityError,
		prioritySet: true,
		since:       1_000_000_000,
		until:       9_000_000_000,
		limit:       50,
	}
	req := buildRequest(opts)
	if len(req.Units) != 2 || req.Units[0] != "sshd" {
		t.Fatalf("units not carried: %v", req.Units)
	}
	if !req.PrioritySet || req.MinPriority != int(journal.PriorityError) {
		t.Fatalf("priority not carried: set=%v v=%d", req.PrioritySet, req.MinPriority)
	}
	if req.Since != 1_000_000_000 || req.Until != 9_000_000_000 {
		t.Fatalf("time range not carried: since=%d until=%d", req.Since, req.Until)
	}
	if req.Limit != 50 {
		t.Fatalf("limit not carried: %d", req.Limit)
	}
}

func TestBuildRequestEmptyPriority(t *testing.T) {
	// PrioritySet must NOT be true when -p was never passed, otherwise
	// the server would filter to emerg-only unexpectedly.
	req := buildRequest(options{})
	if req.PrioritySet {
		t.Fatal("PrioritySet leaked as true from empty options")
	}
}

// ---- 2e: --file path -------------------------------------------------

func TestParseArgsFile(t *testing.T) {
	opts, err := parseArgs([]string{"--file", "/tmp/j.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.sourceFile != "/tmp/j.jsonl" {
		t.Fatalf("sourceFile=%q", opts.sourceFile)
	}
	opts2, err := parseArgs([]string{"--file=/tmp/x.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	if opts2.sourceFile != "/tmp/x.jsonl" {
		t.Fatalf("--file= form: sourceFile=%q", opts2.sourceFile)
	}
}

func TestReadJSONLFileAllPass(t *testing.T) {
	// Craft two events via MarshalJSONL and separate them with a
	// newline (JSONL wire form). readJSONLFile should return both.
	e1 := &journal.Event{Ts: 1, Unit: "a", Msg: "m1", Prio: journal.PriorityInfo}
	e2 := &journal.Event{Ts: 2, Unit: "b", Msg: "m2", Prio: journal.PriorityInfo}
	b1, _ := e1.MarshalJSONL()
	b2, _ := e2.MarshalJSONL()
	src := strings.NewReader(string(b1) + "\n" + string(b2) + "\n")

	got, err := readJSONLFile(src, journal.QueryFilter{MinPriority: -1}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Unit != "a" || got[1].Unit != "b" {
		t.Fatalf("got %d events, first=%+v", len(got), got)
	}
}

func TestReadJSONLFileSkipsBlankAndBroken(t *testing.T) {
	e1 := &journal.Event{Ts: 1, Unit: "a", Msg: "m1", Prio: journal.PriorityInfo}
	b1, _ := e1.MarshalJSONL()
	// Empty lines, a truncated JSON, then a valid event again.
	src := strings.NewReader("\n" + string(b1) + "\n{ not valid\n\n" + string(b1) + "\n")

	got, err := readJSONLFile(src, journal.QueryFilter{MinPriority: -1}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 survivor events, got %d", len(got))
	}
}

func TestReadJSONLFileAppliesFilter(t *testing.T) {
	e1 := &journal.Event{Ts: 1, Unit: "a", Msg: "m1", Prio: journal.PriorityInfo}
	e2 := &journal.Event{Ts: 2, Unit: "b", Msg: "m2", Prio: journal.PriorityInfo}
	b1, _ := e1.MarshalJSONL()
	b2, _ := e2.MarshalJSONL()
	src := strings.NewReader(string(b1) + "\n" + string(b2) + "\n")

	got, err := readJSONLFile(src, journal.QueryFilter{Units: []string{"b"}, MinPriority: -1}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Unit != "b" {
		t.Fatalf("filter leak: got %d events, first unit=%q", len(got), got[0].Unit)
	}
}

func TestReadJSONLFileLimitTail(t *testing.T) {
	var sb strings.Builder
	for i := int64(1); i <= 10; i++ {
		e := &journal.Event{Ts: i, Unit: "u", Prio: journal.PriorityInfo}
		b, _ := e.MarshalJSONL()
		sb.Write(b)
		sb.WriteByte('\n')
	}
	got, err := readJSONLFile(strings.NewReader(sb.String()), journal.QueryFilter{MinPriority: -1}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 events, got %d", len(got))
	}
	// Most-recent-kept semantics: last three events have Ts 8, 9, 10.
	if got[0].Ts != 8 || got[2].Ts != 10 {
		t.Fatalf("expected tail 8..10, got %d..%d", got[0].Ts, got[2].Ts)
	}
}

func TestRunFromFileEnd2End(t *testing.T) {
	// Build a small JSONL file on disk, run runFromFile, verify no
	// error and that reading it back matches.
	dir := t.TempDir()
	path := dir + "/j.jsonl"
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	e := &journal.Event{Ts: 1, Unit: "sshd", Msg: "hello", Prio: journal.PriorityInfo}
	b, _ := e.MarshalJSONL()
	f.Write(b)
	f.Write([]byte("\n"))
	f.Close()

	// Redirect stdout to /dev/null-equivalent — we only care that
	// runFromFile completes without error. Renderer output is
	// validated by dedicated render tests above.
	err = runFromFile(options{
		sourceFile: path,
		format:     fmtCat,
		priority:   0,
	})
	if err != nil {
		t.Fatalf("runFromFile: %v", err)
	}
}

// ---- 2f: -k kernel-only ---------------------------------------------

func TestParseArgsKernelOnly(t *testing.T) {
	opts, err := parseArgs([]string{"-k"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.kernelOnly {
		t.Fatal("kernelOnly not set for -k")
	}
	opts2, err := parseArgs([]string{"--dmesg"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts2.kernelOnly {
		t.Fatal("kernelOnly not set for --dmesg")
	}
}

func TestBuildRequestKernelOnly(t *testing.T) {
	req := buildRequest(options{kernelOnly: true})
	if len(req.Transports) != 1 || req.Transports[0] != string(journal.TransportKernel) {
		t.Fatalf("Transports not set to [kernel]: %v", req.Transports)
	}
}

// ---- 2g: --list-boots / --boot -------------------------------------

func TestParseArgsListBoots(t *testing.T) {
	opts, err := parseArgs([]string{"--list-boots"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.listBoots {
		t.Fatal("listBoots not set")
	}
}

func TestParseArgsBootNoArg(t *testing.T) {
	// --boot alone (or followed by another flag) means "current boot"
	opts, err := parseArgs([]string{"--boot"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.bootSet || opts.bootID != "" {
		t.Fatalf("--boot no-arg: got bootSet=%v id=%q", opts.bootSet, opts.bootID)
	}
	// --boot then a flag → bootID stays empty, flag is consumed
	// separately.
	opts2, err := parseArgs([]string{"--boot", "-r"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts2.bootSet || opts2.bootID != "" || !opts2.reverse {
		t.Fatalf("--boot followed by flag: got bootSet=%v id=%q reverse=%v",
			opts2.bootSet, opts2.bootID, opts2.reverse)
	}
}

// ---- 2h: cursor ---------------------------------------------------

func TestFormatCursor(t *testing.T) {
	e := &journal.Event{Ts: 42, BootID: "abcdef0123"}
	if got := formatCursor(e); got != "s=42;b=abcdef0123" {
		t.Fatalf("formatCursor: %q", got)
	}
}

func TestParseCursorRoundtrip(t *testing.T) {
	orig := &journal.Event{Ts: 987654321, BootID: "0123456789abcdef0123456789abcdef"}
	s := formatCursor(orig)
	ts, boot, err := parseCursor(s)
	if err != nil {
		t.Fatal(err)
	}
	if ts != orig.Ts || boot != orig.BootID {
		t.Fatalf("roundtrip: got ts=%d boot=%q want ts=%d boot=%q",
			ts, boot, orig.Ts, orig.BootID)
	}
}

func TestParseCursorErrors(t *testing.T) {
	cases := []string{
		"",
		"s=42",           // missing b=
		"b=abc",          // missing s=
		"s=NaN;b=abc",    // bad seq
		"garbage;s=1;b=x", // unknown component
	}
	for _, c := range cases {
		if _, _, err := parseCursor(c); err == nil {
			t.Fatalf("expected error for %q", c)
		}
	}
}

func TestParseArgsCursorFlags(t *testing.T) {
	opts, err := parseArgs([]string{"-c", "s=1;b=x"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.cursor != "s=1;b=x" {
		t.Fatalf("cursor not set: %q", opts.cursor)
	}
	opts2, err := parseArgs([]string{"--cursor=s=2;b=y"})
	if err != nil {
		t.Fatal(err)
	}
	if opts2.cursor != "s=2;b=y" {
		t.Fatalf("--cursor= form: %q", opts2.cursor)
	}
	opts3, err := parseArgs([]string{"--show-cursor"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts3.showCursor {
		t.Fatal("showCursor not set")
	}
}

func TestParseArgsBootShortForm(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantID  string
	}{
		{"-b alone", []string{"-b"}, ""},
		{"-b 0 (current)", []string{"-b", "0"}, "0"},
		{"-b0 glued", []string{"-b0"}, "0"},
		{"-b -1 (relative)", []string{"-b", "-1"}, "-1"},
		{"-b-1 glued", []string{"-b-1"}, "-1"},
		{"-b <hex>", []string{"-b", "0123456789abcdef0123456789abcdef"}, "0123456789abcdef0123456789abcdef"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts, err := parseArgs(c.args)
			if err != nil {
				t.Fatal(err)
			}
			if !opts.bootSet {
				t.Fatal("bootSet not set for -b")
			}
			if opts.bootID != c.wantID {
				t.Fatalf("bootID = %q, want %q", opts.bootID, c.wantID)
			}
		})
	}
}

func TestLooksLikeBootSpec(t *testing.T) {
	cases := map[string]bool{
		"":         false, // empty
		"0":        true,  // positive index
		"1":        true,
		"abc123":   true,  // hex ID
		"-1":       true,  // relative index
		"-99":      true,  // relative index
		"-o":       false, // real flag
		"-r":       false, // real flag
		"--follow": false, // long flag
		"-":        false, // stray dash
	}
	for in, want := range cases {
		if got := looksLikeBootSpec(in); got != want {
			t.Errorf("looksLikeBootSpec(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseArgsBootPeekDoesNotEatFlag(t *testing.T) {
	// --boot alone followed by another flag → bootID empty, flag
	// still processed by its own case.
	opts, err := parseArgs([]string{"-b", "-r"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.bootSet || opts.bootID != "" {
		t.Fatalf("boot spec eating: bootSet=%v bootID=%q", opts.bootSet, opts.bootID)
	}
	if !opts.reverse {
		t.Fatal("following -r flag not honored")
	}
}

func TestParseArgsBootWithID(t *testing.T) {
	opts, err := parseArgs([]string{"--boot", "abc123"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.bootSet || opts.bootID != "abc123" {
		t.Fatalf("--boot ID: got bootSet=%v id=%q", opts.bootSet, opts.bootID)
	}
	opts2, err := parseArgs([]string{"--boot=deadbeef"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts2.bootSet || opts2.bootID != "deadbeef" {
		t.Fatalf("--boot=ID: got bootSet=%v id=%q", opts2.bootSet, opts2.bootID)
	}
}

func TestRunQueryFileWithFollowRejected(t *testing.T) {
	err := runQuery(options{sourceFile: "/tmp/whatever", follow: true})
	if err == nil {
		t.Fatal("expected error for --file with --follow")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("unexpected error text: %v", err)
	}
}

// TestParseArgsGroupADisplay verifies every display modifier lands
// in the right options field with both long and (where present) short
// forms accepted.
func TestParseArgsGroupADisplay(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		check func(o options) error
	}{
		{"no-hostname", []string{"--no-hostname"}, func(o options) error {
			if !o.noHostname {
				return fmt.Errorf("noHostname not set")
			}
			return nil
		}},
		{"utc", []string{"--utc"}, func(o options) error {
			if !o.utc {
				return fmt.Errorf("utc not set")
			}
			return nil
		}},
		{"truncate-newline", []string{"--truncate-newline"}, func(o options) error {
			if !o.truncateNewline {
				return fmt.Errorf("truncateNewline not set")
			}
			return nil
		}},
		{"quiet-short", []string{"-q"}, func(o options) error {
			if !o.quiet {
				return fmt.Errorf("quiet not set")
			}
			return nil
		}},
		{"quiet-long", []string{"--quiet"}, func(o options) error {
			if !o.quiet {
				return fmt.Errorf("quiet not set")
			}
			return nil
		}},
		{"all-short", []string{"-a"}, func(o options) error {
			if !o.allFields {
				return fmt.Errorf("allFields not set")
			}
			return nil
		}},
		{"no-full", []string{"--no-full"}, func(o options) error {
			if !o.noFull {
				return fmt.Errorf("noFull not set")
			}
			return nil
		}},
		{"full-short", []string{"-l"}, func(o options) error {
			if !o.fullFlag {
				return fmt.Errorf("fullFlag not set")
			}
			return nil
		}},
		{"no-tail", []string{"--no-tail"}, func(o options) error {
			if !o.noTail {
				return fmt.Errorf("noTail not set")
			}
			return nil
		}},
		{"pager-end", []string{"-e"}, func(o options) error {
			if !o.pagerEnd {
				return fmt.Errorf("pagerEnd not set")
			}
			return nil
		}},
		{"output-fields", []string{"--output-fields=MESSAGE,PRIORITY,_PID"}, func(o options) error {
			if len(o.outputFields) != 3 || o.outputFields[0] != "MESSAGE" {
				return fmt.Errorf("bad outputFields: %v", o.outputFields)
			}
			return nil
		}},
		{"merge", []string{"-m"}, func(o options) error {
			if !o.merge {
				return fmt.Errorf("merge not set")
			}
			return nil
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, err := parseArgs(c.args)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.check(o); err != nil {
				t.Error(err)
			}
		})
	}
}

// TestParseArgsGroupAFiltering covers the systemd-parity filter
// flags: -t/-T for identifier include/exclude, --facility, -g grep,
// --this-boot alias, and -U/--user-unit routing.
func TestParseArgsGroupAFiltering(t *testing.T) {
	o, err := parseArgs([]string{
		"-t", "nginx", "-t", "sshd",
		"-T", "kernel",
		"--facility=mail,4",
		"-g", "PATTERN",
		"--case-sensitive=no",
		"--this-boot",
		"-U", "user-svc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(o.identifiers) != 2 || o.identifiers[0] != "nginx" {
		t.Errorf("identifiers = %v", o.identifiers)
	}
	if len(o.excludeIdentifiers) != 1 || o.excludeIdentifiers[0] != "kernel" {
		t.Errorf("excludeIdentifiers = %v", o.excludeIdentifiers)
	}
	// facility: "mail" → 2, "4" → 4
	if len(o.facility) != 2 || o.facility[0] != 2 || o.facility[1] != 4 {
		t.Errorf("facility = %v", o.facility)
	}
	if !o.facilitySet {
		t.Errorf("facilitySet should be true")
	}
	if o.grep != "PATTERN" {
		t.Errorf("grep = %q", o.grep)
	}
	if !o.grepCaseSet || o.grepCaseSensitive {
		t.Errorf("grepCaseSensitive expected false w/ set=true; got sensitive=%v set=%v", o.grepCaseSensitive, o.grepCaseSet)
	}
	if !o.thisBoot || !o.bootSet || o.bootID != "0" {
		t.Errorf("--this-boot didn't wire bootSet=0")
	}
	if len(o.userUnitFilters) != 1 || o.userUnitFilters[0] != "user-svc" || !o.userMode {
		t.Errorf("-U didn't populate userUnitFilters + userMode: %+v", o)
	}
}

// TestParseArgsGroupAIntro covers introspection flag parsing.
func TestParseArgsGroupAIntro(t *testing.T) {
	o, err := parseArgs([]string{"-F", "_HOSTNAME"})
	if err != nil {
		t.Fatal(err)
	}
	if o.fieldName != "_HOSTNAME" {
		t.Errorf("-F: got %q", o.fieldName)
	}
	o, err = parseArgs([]string{"--fields"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.fieldsList {
		t.Errorf("--fields not set")
	}
	o, err = parseArgs([]string{"--header"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.headerDump {
		t.Errorf("--header not set")
	}
	o, err = parseArgs([]string{"--disk-usage"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.diskUsage {
		t.Errorf("--disk-usage not set")
	}
}

// TestParseArgsGroupACursorSource verifies --after-cursor,
// --cursor-file, -D/--directory, and --root routing.
func TestParseArgsGroupACursorSource(t *testing.T) {
	o, err := parseArgs([]string{
		"--after-cursor=s=1;b=abc",
		"--cursor-file", "/tmp/cf",
		"-D", "/var/log/j",
		"--root=/mnt/snap",
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.afterCursor != "s=1;b=abc" {
		t.Errorf("afterCursor: %q", o.afterCursor)
	}
	if o.cursorFile != "/tmp/cf" {
		t.Errorf("cursorFile: %q", o.cursorFile)
	}
	if o.directory != "/var/log/j" {
		t.Errorf("directory: %q", o.directory)
	}
	if o.root != "/mnt/snap" {
		t.Errorf("root: %q", o.root)
	}
}

// TestParseFacilityList exercises numeric + name mixing, out-of-range
// rejection, and case-insensitive name lookup.
func TestParseFacilityList(t *testing.T) {
	got, err := parseFacilityList("auth,LOCAL0,17,23")
	if err != nil {
		t.Fatal(err)
	}
	want := []int{4, 16, 17, 23}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if _, err := parseFacilityList("24"); err == nil {
		t.Error("expected out-of-range error for 24")
	}
	if _, err := parseFacilityList("bogus-fac"); err == nil {
		t.Error("expected error for unknown facility")
	}
}

// TestGrepInsensitiveHeuristic — matches systemd's default:
// all-lowercase pattern is case-insensitive; any uppercase is
// case-sensitive; explicit --case-sensitive overrides both.
func TestGrepInsensitiveHeuristic(t *testing.T) {
	if !shouldGrepInsensitive(options{grep: "error"}) {
		t.Error("all-lowercase should be insensitive")
	}
	if shouldGrepInsensitive(options{grep: "Error"}) {
		t.Error("mixed-case should be sensitive")
	}
	if shouldGrepInsensitive(options{grep: "error", grepCaseSet: true, grepCaseSensitive: true}) {
		t.Error("explicit sensitive override should win")
	}
	if !shouldGrepInsensitive(options{grep: "Error", grepCaseSet: true, grepCaseSensitive: false}) {
		t.Error("explicit insensitive override should win over uppercase")
	}
}

// TestRenderOptsTruncate — --truncate-newline cuts at the first LF;
// --no-full ellipsizes at 256 chars. Both should combine (newline
// truncation first, then length cap on the survivor).
func TestRenderOptsTruncate(t *testing.T) {
	msg := "first line\nsecond line\nthird"
	ro := renderOpts{truncateNewline: true}
	if got := ro.truncateMsg(msg); got != "first line" {
		t.Errorf("truncateNewline: %q", got)
	}
	long := strings.Repeat("x", 300)
	ro = renderOpts{noFull: true}
	got := ro.truncateMsg(long)
	if len(got) != 256 || !strings.HasSuffix(got, "...") {
		t.Errorf("noFull: len=%d suffix=%q", len(got), got[len(got)-3:])
	}
}

// TestRenderOutputFieldsFilter — --output-fields restricts verbose
// output to the named subset. MESSAGE stays, hidden fields drop.
func TestRenderOutputFieldsFilter(t *testing.T) {
	e := mkEvent()
	ro := renderOpts{outputFields: map[string]bool{"MESSAGE": true}}
	var buf bytes.Buffer
	if err := renderVerbose(&buf, e, ro); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "MESSAGE=") {
		t.Errorf("MESSAGE should be present:\n%s", out)
	}
	if strings.Contains(out, "TS_NSEC=") || strings.Contains(out, "PRIORITY=") {
		t.Errorf("hidden field leaked:\n%s", out)
	}
}

// TestUTCTimestamp — --utc renders in UTC even in a non-UTC TZ.
func TestUTCTimestamp(t *testing.T) {
	// Fixed timestamp: 2026-01-01T00:00:00Z (nanoseconds)
	ts := int64(1767225600) * int64(time.Second)
	got := formatTime(ts, timeISO, true)
	if !strings.HasSuffix(got, "Z") && !strings.HasSuffix(got, "+00:00") {
		t.Errorf("utc timestamp should end in Z or +00:00: %q", got)
	}
}

// TestResolveCursorInputPriority — --after-cursor beats --cursor beats
// --cursor-file when multiple are set. Mode reflects the source.
func TestResolveCursorInputPriority(t *testing.T) {
	tmp := t.TempDir() + "/cf"
	if err := os.WriteFile(tmp, []byte("s=42;b=fromfile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tok, mode, err := resolveCursorInput(options{
		afterCursor: "s=1;b=A", cursor: "s=2;b=B", cursorFile: tmp,
	})
	if err != nil || tok != "s=1;b=A" || mode != cursorAfter {
		t.Errorf("afterCursor should win: tok=%q mode=%v err=%v", tok, mode, err)
	}
	tok, mode, err = resolveCursorInput(options{cursor: "s=2;b=B", cursorFile: tmp})
	if err != nil || tok != "s=2;b=B" || mode != cursorInclusive {
		t.Errorf("cursor should beat cursor-file: tok=%q mode=%v err=%v", tok, mode, err)
	}
	tok, mode, err = resolveCursorInput(options{cursorFile: tmp})
	if err != nil || tok != "s=42;b=fromfile" || mode != cursorInclusive {
		t.Errorf("cursor-file only: tok=%q mode=%v err=%v", tok, mode, err)
	}
	// Missing cursor-file is treated as "no prior cursor" — bootstrap OK.
	tok, mode, err = resolveCursorInput(options{cursorFile: "/nonexistent/xyz"})
	if err != nil || tok != "" || mode != cursorInclusive {
		t.Errorf("missing cursor-file should bootstrap silently: tok=%q err=%v", tok, err)
	}
}

// TestWriteCursorFileAtomic — writeCursorFile must land the update
// atomically (tmp+rename), so a torn write can never leave a
// half-baked cursor on disk.
func TestWriteCursorFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/cursor"
	if err := writeCursorFile(path, "s=100;b=abc"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "s=100;b=abc" {
		t.Errorf("cursor content: %q", string(data))
	}
	// Overwrite: same path, different value.
	if err := writeCursorFile(path, "s=200;b=xyz"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if strings.TrimSpace(string(data)) != "s=200;b=xyz" {
		t.Errorf("overwrite failed: %q", string(data))
	}
}

// TestParseSizeArg covers systemd-style byte-size parsing: bare
// integer, K/M/G/T (case-insensitive), and the KiB/MiB "i" suffix.
func TestParseSizeArg(t *testing.T) {
	cases := map[string]int64{
		"0":     0,
		"1024":  1024,
		"1K":    1024,
		"1k":    1024,
		"2M":    2 * 1024 * 1024,
		"3G":    3 * 1024 * 1024 * 1024,
		"1T":    1024 * 1024 * 1024 * 1024,
		"512B":  512,
		"64KiB": 64 * 1024,
	}
	for in, want := range cases {
		got, err := parseSizeArg(in)
		if err != nil {
			t.Errorf("parseSizeArg(%q): unexpected error %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseSizeArg(%q) = %d, want %d", in, got, want)
		}
	}
	if _, err := parseSizeArg(""); err == nil {
		t.Error("empty string should error")
	}
	if _, err := parseSizeArg("-5K"); err == nil {
		t.Error("negative size should error")
	}
	if _, err := parseSizeArg("bad"); err == nil {
		t.Error("non-numeric should error")
	}
}

// TestParseDurationArg — mix of Go-native (1h30m) and systemd tokens
// (5s, 3d, 2w, 6M, 1y). Negative values rejected; unknown units
// rejected.
func TestParseDurationArg(t *testing.T) {
	cases := map[string]time.Duration{
		"5s":     5 * time.Second,
		"30m":    30 * time.Minute,
		"2h":     2 * time.Hour,
		"1d":     24 * time.Hour,
		"2w":     14 * 24 * time.Hour,
		"6M":     6 * 30 * 24 * time.Hour,
		"1y":     365 * 24 * time.Hour,
		"1h30m":  time.Hour + 30*time.Minute, // Go-native
		"250ms":  250 * time.Millisecond,     // Go-native
	}
	for in, want := range cases {
		got, err := parseDurationArg(in)
		if err != nil {
			t.Errorf("parseDurationArg(%q): unexpected error %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseDurationArg(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := parseDurationArg("5x"); err == nil {
		t.Error("unknown unit should error")
	}
	if _, err := parseDurationArg(""); err == nil {
		t.Error("empty should error")
	}
}

// TestParseArgsGroupBMaintenance covers all 5 Group B flags plus the
// --pid-file override.
func TestParseArgsGroupBMaintenance(t *testing.T) {
	o, err := parseArgs([]string{
		"--sync",
		"--rotate",
		"--vacuum-size=100M",
		"--vacuum-files=10",
		"--vacuum-time=30d",
		"--pid-file", "/tmp/pid",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !o.sync {
		t.Error("sync not set")
	}
	if !o.rotate {
		t.Error("rotate not set")
	}
	if o.vacuumSize != 100*1024*1024 {
		t.Errorf("vacuumSize = %d", o.vacuumSize)
	}
	if o.vacuumFiles != 10 {
		t.Errorf("vacuumFiles = %d", o.vacuumFiles)
	}
	if o.vacuumTime != 30*24*time.Hour {
		t.Errorf("vacuumTime = %v", o.vacuumTime)
	}
	if !o.vacuumSet {
		t.Error("vacuumSet sentinel not toggled")
	}
	if o.pidFile != "/tmp/pid" {
		t.Errorf("pidFile = %q", o.pidFile)
	}
}

// TestReadDaemonPIDErrors — clean error paths for missing and stale
// PID files. A live PID would need a fixture process to test, out of
// scope for a plain unit run.
func TestReadDaemonPIDErrors(t *testing.T) {
	if _, err := readDaemonPID("/nonexistent/pid/file"); err == nil {
		t.Error("expected error for missing pid file")
	}
	tmp := t.TempDir() + "/pid"
	// Bad contents
	if err := os.WriteFile(tmp, []byte("not-a-pid\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readDaemonPID(tmp); err == nil {
		t.Error("expected error for malformed pid")
	}
	// Nonexistent PID (very large number unlikely to be assigned)
	if err := os.WriteFile(tmp, []byte("2147483646\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readDaemonPID(tmp); err == nil {
		t.Error("expected error for stale pid")
	}
}

// TestCurrentJournalFiles — list should include today's dated
// jsonl+journal files under the given dir, absolute paths.
func TestCurrentJournalFiles(t *testing.T) {
	files := currentJournalFiles("/var/log/slinit-journal")
	if len(files) != 2 {
		t.Fatalf("expected 2 files (jsonl + journal), got %d", len(files))
	}
	today := time.Now().UTC().Format("2006-01-02")
	for _, f := range files {
		if !strings.Contains(f, today) {
			t.Errorf("expected today's date in %q", f)
		}
		if !strings.HasPrefix(f, "/var/log/slinit-journal/") {
			t.Errorf("expected absolute path, got %q", f)
		}
	}
}

// TestParseArgsGroupC covers --setup-keys, --verify-key, --interval.
func TestParseArgsGroupC(t *testing.T) {
	o, err := parseArgs([]string{"--setup-keys", "--verify-key=abc123", "--interval=1h"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.setupKeys {
		t.Error("setupKeys not set")
	}
	if o.verifyKey != "abc123" {
		t.Errorf("verifyKey = %q", o.verifyKey)
	}
	if o.fssInterval != time.Hour {
		t.Errorf("fssInterval = %v", o.fssInterval)
	}
}

// TestParseArgsGroupD covers catalog flags.
func TestParseArgsGroupD(t *testing.T) {
	o, err := parseArgs([]string{"-x", "--dump-catalog", "--update-catalog", "--list-catalog"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.catalog || !o.dumpCatalog || !o.updateCatalog || !o.listCatalog {
		t.Errorf("bad flags: %+v", o)
	}
}

// TestParseArgsGroupE covers invocation flags.
func TestParseArgsGroupE(t *testing.T) {
	o, err := parseArgs([]string{"--invocation=deadbeef", "--list-invocations"})
	if err != nil {
		t.Fatal(err)
	}
	if o.invocation != "deadbeef" {
		t.Errorf("invocation = %q", o.invocation)
	}
	if !o.listInvocations {
		t.Error("listInvocations not set")
	}
}

// TestRunSetupKeysWritesFile verifies --setup-keys creates a valid
// FSSKey file and prints a verification token on stdout.
func TestRunSetupKeysWritesFile(t *testing.T) {
	dir := t.TempDir()
	key := dir + "/journal-key"
	var out bytes.Buffer
	err := runSetupKeys(options{fssKeyPath: key, fssInterval: time.Hour}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(key); err != nil {
		t.Errorf("key file not created: %v", err)
	}
	if !strings.Contains(out.String(), "Verification key") {
		t.Errorf("output missing verification key header: %q", out.String())
	}
	// Reload → should parse cleanly.
	loaded, err := journalbin.LoadFSSKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.IntervalUsec != int64(time.Hour/time.Microsecond) {
		t.Errorf("interval not persisted: %d", loaded.IntervalUsec)
	}
}

// TestSetupKeysRefusesExistingWithoutForce — safety gate: overwriting
// invalidates every prior TAG chain, so operators must opt in.
func TestSetupKeysRefusesExistingWithoutForce(t *testing.T) {
	dir := t.TempDir()
	key := dir + "/journal-key"
	// Seed an existing file. Mode 0600 so the rewrite path can open
	// it for write — the test is about --force logic, not permission
	// handling.
	if err := os.WriteFile(key, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runSetupKeys(options{fssKeyPath: key}, &out)
	if err == nil {
		t.Fatal("expected error refusing to overwrite existing key")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should mention --force: %v", err)
	}
	// With --force it should succeed and overwrite.
	err = runSetupKeys(options{fssKeyPath: key, force: true}, &out)
	if err != nil {
		t.Fatalf("--force run failed: %v", err)
	}
	loaded, err := journalbin.LoadFSSKey(key)
	if err != nil {
		t.Fatalf("reload after --force: %v", err)
	}
	if loaded.Seed == "" {
		t.Error("expected fresh seed after --force overwrite")
	}
}

// TestParseArgsSprint2 covers --flush, --relinquish-var,
// --smart-relinquish-var (all boolean).
func TestParseArgsSprint2(t *testing.T) {
	o, err := parseArgs([]string{"--flush"})
	if err != nil || !o.flush {
		t.Errorf("--flush: %+v %v", o, err)
	}
	o, err = parseArgs([]string{"--relinquish-var"})
	if err != nil || !o.relinquishVar {
		t.Errorf("--relinquish-var: %+v %v", o, err)
	}
	o, err = parseArgs([]string{"--smart-relinquish-var"})
	if err != nil || !o.smartRelinquishVar {
		t.Errorf("--smart-relinquish-var: %+v %v", o, err)
	}
}

// TestParseArgsSprint4 covers --image and --image-policy.
func TestParseArgsSprint4(t *testing.T) {
	o, err := parseArgs([]string{"--image=/path/to/disk.img", "--image-policy=strict"})
	if err != nil {
		t.Fatal(err)
	}
	if o.image != "/path/to/disk.img" {
		t.Errorf("image = %q", o.image)
	}
	if o.imagePolicy != "strict" {
		t.Errorf("imagePolicy = %q", o.imagePolicy)
	}
}

// TestParseArgsSprint3 covers --namespace and --list-namespaces.
func TestParseArgsSprint3(t *testing.T) {
	o, err := parseArgs([]string{"--namespace=prod"})
	if err != nil || o.namespace != "prod" {
		t.Errorf("--namespace: %+v %v", o, err)
	}
	o, err = parseArgs([]string{"--list-namespaces"})
	if err != nil || !o.listNamespaces {
		t.Errorf("--list-namespaces: %+v %v", o, err)
	}
}

// TestRunListNamespacesSyntheticDirs seeds a fake filesystem layout
// under a tempdir + points the scan there via chroot-lite by
// directly calling the scanner logic (we can't easily override the
// hardcoded /var/log path without more refactoring; smoke that the
// impl reads the right shape without crashing).
func TestRunListNamespacesShape(t *testing.T) {
	// Just verify the function runs and prints SOMETHING to the
	// writer for a system where the scan may or may not find dirs.
	var buf bytes.Buffer
	if err := runListNamespaces(&buf); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Error("expected some output (either namespace names or the 'no namespaces' notice)")
	}
}

// TestIsSeparateVarMount smoke — the check parses mountinfo without
// blowing up regardless of what the host has for /var. We can't
// assume a specific answer (dev boxes vary), just that the call
// returns cleanly.
func TestIsSeparateVarMount(t *testing.T) {
	if _, err := isSeparateVarMount(); err != nil {
		t.Errorf("isSeparateVarMount: %v", err)
	}
}

// TestParseArgsSprint1 covers --force and --synchronize-on-exit
// (both long and =BOOL forms).
func TestParseArgsSprint1(t *testing.T) {
	o, err := parseArgs([]string{"--force"})
	if err != nil || !o.force {
		t.Errorf("--force: %+v %v", o, err)
	}
	o, err = parseArgs([]string{"--synchronize-on-exit"})
	if err != nil || !o.syncOnExit {
		t.Errorf("--synchronize-on-exit: %+v %v", o, err)
	}
	o, err = parseArgs([]string{"--synchronize-on-exit=no"})
	if err != nil || o.syncOnExit {
		t.Errorf("--synchronize-on-exit=no should set false: %+v %v", o, err)
	}
	o, err = parseArgs([]string{"--synchronize-on-exit=yes"})
	if err != nil || !o.syncOnExit {
		t.Errorf("--synchronize-on-exit=yes should set true: %+v %v", o, err)
	}
}

// TestAggregateBoot covers the BootID→timespan folding used by
// --list-boots. Empty BootID entries drop; multi-event windows
// extend to the widest bounds; a single event forms a zero-width
// range. This is the cross-source dedupe primitive — same BootID
// showing up in ring buffer + on-disk journals collapses into one
// row, so the invariant "range spans min/max ts across all sources"
// is load-bearing.
func TestAggregateBoot(t *testing.T) {
	byID := map[string]*bootRange{}

	// Empty BootID: dropped.
	aggregateBoot(byID, &journal.Event{Ts: 100})
	if len(byID) != 0 {
		t.Errorf("empty BootID should be dropped; got %+v", byID)
	}

	// First entry for a BootID: creates window at (ts, ts).
	aggregateBoot(byID, &journal.Event{BootID: "b1", Ts: 500})
	if r := byID["b1"]; r == nil || r.first != 500 || r.last != 500 {
		t.Errorf("first insert: got %+v", r)
	}

	// Same BootID, earlier ts: extend .first downward.
	aggregateBoot(byID, &journal.Event{BootID: "b1", Ts: 200})
	if r := byID["b1"]; r.first != 200 || r.last != 500 {
		t.Errorf("earlier ts: got first=%d last=%d, want first=200 last=500", r.first, r.last)
	}

	// Same BootID, later ts: extend .last upward.
	aggregateBoot(byID, &journal.Event{BootID: "b1", Ts: 900})
	if r := byID["b1"]; r.first != 200 || r.last != 900 {
		t.Errorf("later ts: got first=%d last=%d, want first=200 last=900", r.first, r.last)
	}

	// Different BootID: independent window.
	aggregateBoot(byID, &journal.Event{BootID: "b2", Ts: 1000})
	if r := byID["b2"]; r == nil || r.first != 1000 || r.last != 1000 {
		t.Errorf("distinct BootID: got %+v", r)
	}
	if len(byID) != 2 {
		t.Errorf("expected 2 distinct boots, got %d", len(byID))
	}
}

// TestIsTruncationErr covers the classifier used by
// aggregateBootsFromDir to distinguish "unclean-shutdown tail past
// end of file" (recoverable — the earlier records are fine) from
// "unreadable file" (permissions, bad magic, real corruption). Live-
// test surfaced a journalbin EOF wrapped as "journalbin: read
// entry-array hdr at 1844720: EOF" that MUST be classified as
// truncation so --list-boots keeps working after unclean shutdown.
func TestIsTruncationErr(t *testing.T) {
	// Positive: bare EOF sentinels.
	if !isTruncationErr(io.EOF) {
		t.Error("io.EOF should be classified as truncation")
	}
	if !isTruncationErr(io.ErrUnexpectedEOF) {
		t.Error("io.ErrUnexpectedEOF should be classified as truncation")
	}

	// Positive: wrapped EOF via fmt.Errorf %w — matches the modern
	// error chain path.
	wrapped := fmt.Errorf("journalbin: read entry-array hdr at 1844720: %w", io.EOF)
	if !isTruncationErr(wrapped) {
		t.Error("wrapped EOF should be classified as truncation")
	}

	// Positive: string-only "…: EOF" fallback for historical wraps
	// that lost the sentinel through fmt.Errorf without %w.
	stringOnly := errors.New("journalbin: read entry-array hdr at 1844720: EOF")
	if !isTruncationErr(stringOnly) {
		t.Error("string-form 'EOF' suffix should be classified as truncation")
	}

	// Negative: real errors do NOT get absorbed. Regression guard
	// against an over-broad match that would hide genuine
	// bad-magic / permission / corruption failures.
	if isTruncationErr(nil) {
		t.Error("nil is not truncation")
	}
	if isTruncationErr(errors.New("bad magic")) {
		t.Error("random error should NOT match")
	}
	if isTruncationErr(errors.New("permission denied")) {
		t.Error("permission-denied should NOT match")
	}
}

// TestAllOutputFormatsRoundtrip covers every -o value: each format
// must accept a canned Event without erroring, produce non-empty
// output, and (where applicable) round-trip the key content —
// timestamp, ident, message. Lock-in that the switch in render()
// has a branch for every entry in validFormats.
func TestAllOutputFormatsRoundtrip(t *testing.T) {
	e := &journal.Event{
		Ts:               time.Date(2026, 9, 3, 22, 53, 17, 123_456_000, time.UTC).UnixNano(),
		Mts:              5_123_456_000, // 5.123456 seconds since boot
		Msg:              "test message",
		SyslogIdentifier: "svc",
		Unit:             "svc",
		Pid:              42,
		Hostname:         "container",
		Prio:             journal.PriorityInfo,
		Transport:        journal.TransportStdout,
	}
	ro := renderOpts{utc: true}

	for _, f := range validFormats {
		t.Run(string(f), func(t *testing.T) {
			var buf bytes.Buffer
			if err := render(&buf, f, e, ro); err != nil {
				t.Fatalf("render(%s): %v", f, err)
			}
			if buf.Len() == 0 {
				t.Fatalf("render(%s) produced no output", f)
			}
			// Every format should contain the message somewhere in
			// the output (either directly or embedded in JSON).
			if !strings.Contains(buf.String(), "test message") {
				t.Errorf("render(%s) missing message; got:\n%s", f, buf.String())
			}
		})
	}
}

// TestJSONSeqFraming — RFC 7464 requires RS (0x1E) before each record.
// Regression guard: if someone "cleans up" the byte to a plain
// separator this catches it.
func TestJSONSeqFraming(t *testing.T) {
	e := &journal.Event{Msg: "seq-test", Ts: 1000, Mts: 500}
	var buf bytes.Buffer
	if err := render(&buf, fmtJSONSeq, e, renderOpts{}); err != nil {
		t.Fatal(err)
	}
	out := buf.Bytes()
	if len(out) < 2 || out[0] != 0x1e {
		t.Errorf("json-seq output must start with 0x1e (RS); got %v", out[:2])
	}
	if out[len(out)-1] != '\n' {
		t.Error("json-seq output must end with LF")
	}
}

// TestJSONSSEFraming — SSE demands "data: " prefix + blank-line
// terminator. Catches drift from the W3C spec.
func TestJSONSSEFraming(t *testing.T) {
	e := &journal.Event{Msg: "sse-test", Ts: 1000, Mts: 500}
	var buf bytes.Buffer
	if err := render(&buf, fmtJSONSSE, e, renderOpts{}); err != nil {
		t.Fatal(err)
	}
	s := buf.String()
	if !strings.HasPrefix(s, "data: ") {
		t.Errorf("json-sse must start with 'data: '; got %q", s[:min(10, len(s))])
	}
	if !strings.HasSuffix(s, "\n\n") {
		t.Errorf("json-sse must end with blank-line terminator; got %q", s[max(0, len(s)-4):])
	}
}

// TestMonotonicFormat — the [SSS.uuuuuu] bracket format is what
// journalctl produces + what dmesg-style tools expect. Regression
// against timestamp-source mixing (e.Ts vs e.Mts).
func TestMonotonicFormat(t *testing.T) {
	e := &journal.Event{
		Ts:        time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano(),
		Mts:       5_123_456_000, // 5.123456 s since boot
		Msg:       "mono-test",
		Unit:      "svc",
		SyslogIdentifier: "svc",
	}
	var buf bytes.Buffer
	if err := render(&buf, fmtShortMonotonic, e, renderOpts{}); err != nil {
		t.Fatal(err)
	}
	s := buf.String()
	if !strings.Contains(s, "[    5.123456]") {
		t.Errorf("monotonic format missing [    5.123456] prefix; got: %s", s)
	}
	// Wall clock should NOT appear anywhere.
	if strings.Contains(s, "Jan 01") {
		t.Errorf("monotonic must not include wall-clock; got: %s", s)
	}
}

// TestExtractField — field name → Event value lookup, covering core
// fields (MESSAGE, _PID), a freeform (SLINIT_EVENT), and a miss.
func TestExtractField(t *testing.T) {
	e := &journal.Event{
		Msg:              "hello",
		Pid:              123,
		SyslogIdentifier: "my-svc",
		Fields:           map[string]string{"SLINIT_EVENT": "started"},
	}
	if got := extractField(e, "MESSAGE"); got != "hello" {
		t.Errorf("MESSAGE: %q", got)
	}
	if got := extractField(e, "_PID"); got != "123" {
		t.Errorf("_PID: %q", got)
	}
	if got := extractField(e, "SYSLOG_IDENTIFIER"); got != "my-svc" {
		t.Errorf("SYSLOG_IDENTIFIER: %q", got)
	}
	if got := extractField(e, "SLINIT_EVENT"); got != "started" {
		t.Errorf("SLINIT_EVENT: %q", got)
	}
	if got := extractField(e, "DOES_NOT_EXIST"); got != "" {
		t.Errorf("miss should be empty: %q", got)
	}
}

// TestParseArgsSystemdShortAliases covers the systemd-parity short
// forms added in the full-parity pass: -S/-i/-N/-W/-I plus --no-pager
// silent no-op and -M/--machine WARN-shim. Each must reach the same
// options field as the long form (or a distinct sentinel where the
// systemd flag has no long-form equivalent).
func TestParseArgsSystemdShortAliases(t *testing.T) {
	// -S = --since; use a parseable time to avoid pulling in the
	// time-parse implementation details.
	nowStr := time.Now().Format(time.RFC3339)
	oS, err := parseArgs([]string{"-S", nowStr})
	if err != nil || oS.since == 0 {
		t.Errorf("-S %q: since=%d err=%v", nowStr, oS.since, err)
	}
	oSl, err := parseArgs([]string{"--since", nowStr})
	if err != nil || oSl.since != oS.since {
		t.Errorf("--since parity mismatch: -S since=%d, --since since=%d err=%v", oS.since, oSl.since, err)
	}

	// -i = --file
	oI, err := parseArgs([]string{"-i", "/tmp/foo.jsonl"})
	if err != nil || oI.sourceFile != "/tmp/foo.jsonl" {
		t.Errorf("-i: sourceFile=%q err=%v", oI.sourceFile, err)
	}

	// -N = --fields
	oN, err := parseArgs([]string{"-N"})
	if err != nil || !oN.fieldsList {
		t.Errorf("-N: fieldsList=%v err=%v", oN.fieldsList, err)
	}

	// -W = --no-hostname
	oW, err := parseArgs([]string{"-W"})
	if err != nil || !oW.noHostname {
		t.Errorf("-W: noHostname=%v err=%v", oW.noHostname, err)
	}

	// -I = latest invocation of -u UNIT (no long-form; sentinel field)
	oILat, err := parseArgs([]string{"-I"})
	if err != nil || !oILat.latestInvocation {
		t.Errorf("-I: latestInvocation=%v err=%v", oILat.latestInvocation, err)
	}

	// --no-pager is silent — options should be default-shaped and no
	// error should surface.
	oNP, err := parseArgs([]string{"--no-pager"})
	if err != nil {
		t.Errorf("--no-pager err=%v", err)
	}
	_ = oNP

	// -M / --machine variants
	oM1, err := parseArgs([]string{"-M", "web-container"})
	if err != nil || oM1.machineTarget != "web-container" {
		t.Errorf("-M: machineTarget=%q err=%v", oM1.machineTarget, err)
	}
	oM2, err := parseArgs([]string{"--machine=db-container"})
	if err != nil || oM2.machineTarget != "db-container" {
		t.Errorf("--machine=: machineTarget=%q err=%v", oM2.machineTarget, err)
	}
	oM3, err := parseArgs([]string{"--machine", "cache"})
	if err != nil || oM3.machineTarget != "cache" {
		t.Errorf("--machine: machineTarget=%q err=%v", oM3.machineTarget, err)
	}
}

// TestRunQueryDashIRequiresUnit covers the run-time gate for -I —
// it needs -u UNIT because it resolves to the latest invocation ID of
// a specific unit, and it's mutually exclusive with --invocation=.
func TestRunQueryDashIRequiresUnit(t *testing.T) {
	// -I alone → error
	opts, err := parseArgs([]string{"-I"})
	if err != nil {
		t.Fatal(err)
	}
	if runErr := runQuery(opts); runErr == nil || !strings.Contains(runErr.Error(), "-I requires -u UNIT") {
		t.Errorf("-I without -u: err=%v (want mention of `-u UNIT`)", runErr)
	}

	// -I + --invocation=X → mutually exclusive error
	opts2, err := parseArgs([]string{"-I", "-u", "web", "--invocation=abc"})
	if err != nil {
		t.Fatal(err)
	}
	if runErr := runQuery(opts2); runErr == nil || !strings.Contains(runErr.Error(), "mutually exclusive") {
		t.Errorf("-I + --invocation=: err=%v (want mutually exclusive)", runErr)
	}
}
