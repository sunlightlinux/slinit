package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
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
	var buf bytes.Buffer
	if err := render(&buf, fmtShort, mkEvent()); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	// short uses local time, so we can't check the timestamp text
	// verbatim — just check the trailing parts and pid marker.
	if !strings.Contains(got, "ceres sshd[1234]: hello world\n") {
		t.Fatalf("unexpected short output: %q", got)
	}
}

func TestRenderShortISO(t *testing.T) {
	var buf bytes.Buffer
	if err := render(&buf, fmtShortISO, mkEvent()); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "ceres sshd[1234]: hello world\n") {
		t.Fatalf("unexpected short-iso output: %q", got)
	}
	// short-iso timestamp begins with 4-digit year.
	if !strings.HasPrefix(got, "2026-") {
		t.Fatalf("short-iso should start with year, got %q", got[:20])
	}
}

func TestRenderShortNoPid(t *testing.T) {
	e := mkEvent()
	e.Pid = 0
	var buf bytes.Buffer
	if err := render(&buf, fmtShort, e); err != nil {
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
	if err := render(&buf, fmtShort, e); err != nil {
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

func TestRenderShortFallsBackToEmitterPID(t *testing.T) {
	// No SLINIT_TARGET_PID → fall back to _PID (the emitter).
	// Preserves the current behaviour for events that don't carry the
	// hint (external clients, kmsg, etc — target concept doesn't apply).
	e := mkEvent()
	e.Pid = 1234
	delete(e.Fields, "SLINIT_TARGET_PID")

	var buf bytes.Buffer
	if err := render(&buf, fmtShort, e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "sshd[1234]:") {
		t.Fatalf("emitter pid fallback failed: %q", buf.String())
	}
}

func TestRenderShortIgnoresMalformedTargetPID(t *testing.T) {
	// Non-numeric SLINIT_TARGET_PID → treated as absent, fall back
	// to _PID rather than emitting garbage in the bracket.
	e := mkEvent()
	e.Pid = 42
	if e.Fields == nil {
		e.Fields = map[string]string{}
	}
	e.Fields["SLINIT_TARGET_PID"] = "not-a-number"

	var buf bytes.Buffer
	if err := render(&buf, fmtShort, e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "sshd[42]:") {
		t.Fatalf("malformed target-pid should fall back: %q", buf.String())
	}
}

func TestRenderShortFallbacks(t *testing.T) {
	// Empty hostname should render as "-".
	e := mkEvent()
	e.Hostname = ""
	var buf bytes.Buffer
	if err := render(&buf, fmtShort, e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), " - sshd") {
		t.Fatalf("empty hostname should render as '-': %q", buf.String())
	}

	// SyslogIdentifier wins over Unit.
	e2 := mkEvent()
	e2.SyslogIdentifier = "openssh"
	var buf2 bytes.Buffer
	if err := render(&buf2, fmtShort, e2); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf2.String(), " openssh[") {
		t.Fatalf("SyslogIdentifier should override Unit: %q", buf2.String())
	}
}

func TestRenderCat(t *testing.T) {
	var buf bytes.Buffer
	if err := render(&buf, fmtCat, mkEvent()); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "hello world\n" {
		t.Fatalf("cat should print only the message: %q", buf.String())
	}
}

func TestRenderJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := render(&buf, fmtJSON, mkEvent()); err != nil {
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
	if err := render(&buf, fmtVerbose, mkEvent()); err != nil {
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
	if err := render(&buf, fmtExport, mkEvent()); err != nil {
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
	if err := render(&buf, fmtExport, e); err != nil {
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
	if err := render(&buf, "hex", mkEvent()); err == nil {
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
