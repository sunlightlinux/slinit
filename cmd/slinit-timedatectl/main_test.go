package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseArgs_Defaults(t *testing.T) {
	opts, err := parseArgs(nil)
	if err != nil {
		t.Fatalf("parseArgs(nil): %v", err)
	}
	if opts.cmd != "status" {
		t.Errorf("default cmd = %q, want status", opts.cmd)
	}
}

func TestParseArgs_Subcommands(t *testing.T) {
	cases := []struct {
		in  []string
		cmd string
		nar int
	}{
		{[]string{"status"}, "status", 0},
		{[]string{"show"}, "show", 0},
		{[]string{"set-time", "now"}, "set-time", 1},
		{[]string{"set-timezone", "Europe/Bucharest"}, "set-timezone", 1},
		{[]string{"list-timezones"}, "list-timezones", 0},
		{[]string{"set-local-rtc", "no"}, "set-local-rtc", 1},
		{[]string{"set-ntp", "yes"}, "set-ntp", 1},
	}
	for _, c := range cases {
		opts, err := parseArgs(c.in)
		if err != nil {
			t.Fatalf("parseArgs(%v): %v", c.in, err)
		}
		if opts.cmd != c.cmd {
			t.Errorf("parseArgs(%v).cmd = %q, want %q", c.in, opts.cmd, c.cmd)
		}
		if len(opts.args) != c.nar {
			t.Errorf("parseArgs(%v).args = %v, want %d", c.in, opts.args, c.nar)
		}
	}
}

func TestParseArgs_Flags(t *testing.T) {
	opts, err := parseArgs([]string{
		"--no-pager", "--adjust-system-clock", "--json=short",
		"-p", "Timezone", "set-local-rtc", "yes",
	})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if !opts.noPager || !opts.adjustClock || opts.jsonMode != "short" {
		t.Errorf("flags not applied: %+v", opts)
	}
	if len(opts.property) != 1 || opts.property[0] != "Timezone" {
		t.Errorf("property = %v, want [Timezone]", opts.property)
	}
	if opts.cmd != "set-local-rtc" || len(opts.args) != 1 {
		t.Errorf("cmd/args wrong: %q %v", opts.cmd, opts.args)
	}
}

func TestParseArgs_JSONReject(t *testing.T) {
	if _, err := parseArgs([]string{"--json=bogus"}); err == nil {
		t.Error("bogus JSON mode should error")
	}
	if _, err := parseArgs([]string{"--nope"}); err == nil {
		t.Error("unknown flag should error")
	}
}

func TestParseTime(t *testing.T) {
	now := func() time.Time {
		return time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	}
	cases := []struct {
		in   string
		want time.Time
		name string
	}{
		{"now", now(), "special-now"},
		{"@1700000000", time.Unix(1700000000, 0), "epoch"},
		{"@1700000000.5", time.Unix(1700000000, 500_000_000), "epoch-fraction"},
		{"2024-01-15T14:30:00Z",
			time.Date(2024, 1, 15, 14, 30, 0, 0, time.UTC), "rfc3339"},
		{"+5min", now().Add(5 * time.Minute), "relative-min"},
		{"-2h", now().Add(-2 * time.Hour), "relative-h"},
		{"+1d", now().Add(24 * time.Hour), "relative-day"},
	}
	for _, c := range cases {
		got, err := parseTime(c.in, now)
		if err != nil {
			t.Fatalf("[%s] parseTime(%q): %v", c.name, c.in, err)
		}
		if !got.Equal(c.want) {
			t.Errorf("[%s] parseTime(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
	if _, err := parseTime("not-a-time", now); err == nil {
		t.Error("nonsense should error")
	}
}

func TestParseBool(t *testing.T) {
	trues := []string{"1", "yes", "YES", "true", "True", "on"}
	falses := []string{"0", "no", "false", "off", "OFF"}
	for _, s := range trues {
		got, err := parseBool(s)
		if err != nil || !got {
			t.Errorf("parseBool(%q) = %v,%v; want true,nil", s, got, err)
		}
	}
	for _, s := range falses {
		got, err := parseBool(s)
		if err != nil || got {
			t.Errorf("parseBool(%q) = %v,%v; want false,nil", s, got, err)
		}
	}
	if _, err := parseBool("maybe"); err == nil {
		t.Error("bogus bool should error")
	}
}

func TestAdjtime_ReadMissing(t *testing.T) {
	restore := swap(&adjtimePath, filepath.Join(t.TempDir(), "missing"))
	defer restore()
	mode, err := readAdjtimeMode()
	if err != nil {
		t.Fatalf("readAdjtimeMode: %v", err)
	}
	if mode != "UTC" {
		t.Errorf("missing adjtime → %q, want UTC (kernel default)", mode)
	}
	if rtcInLocal() {
		t.Error("rtcInLocal() should be false when adjtime missing")
	}
}

func TestAdjtime_ReadWriteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "adjtime")
	restore := swap(&adjtimePath, path)
	defer restore()

	os.WriteFile(path, []byte("0.125000 100000 0.500000\n50000\nUTC\n"), 0o644)
	if mode, _ := readAdjtimeMode(); mode != "UTC" {
		t.Errorf("initial mode = %q, want UTC", mode)
	}
	if err := writeAdjtimeMode("LOCAL"); err != nil {
		t.Fatalf("writeAdjtimeMode: %v", err)
	}
	body, _ := os.ReadFile(path)
	text := string(body)
	if !strings.Contains(text, "0.125000 100000 0.500000") ||
		!strings.Contains(text, "\n50000\n") ||
		!strings.Contains(text, "\nLOCAL\n") {
		t.Errorf("adjtime after rewrite missing preserved fields:\n%s", text)
	}
	if mode, _ := readAdjtimeMode(); mode != "LOCAL" {
		t.Errorf("re-read mode = %q, want LOCAL", mode)
	}
}

func TestAdjtime_WriteMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "adjtime")
	restore := swap(&adjtimePath, path)
	defer restore()
	if err := writeAdjtimeMode("LOCAL"); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "0.000000") || !strings.Contains(string(got), "LOCAL") {
		t.Errorf("fresh adjtime unexpected:\n%s", got)
	}
}

func TestAdjtime_WriteBogusRejected(t *testing.T) {
	if err := writeAdjtimeMode("wat"); err == nil {
		t.Error("bogus mode should error")
	}
}

func TestReadZoneTab(t *testing.T) {
	dir := t.TempDir()
	tab := filepath.Join(dir, "zone.tab")
	// Real zone.tab format: CC±LatLong Zone [Comments]
	body := "# zone.tab test fixture\n" +
		"RO\t+4426+02606\tEurope/Bucharest\n" +
		"US\t+404251-0740023\tAmerica/New_York\n" +
		"\n" +
		"# a comment\n"
	os.WriteFile(tab, []byte(body), 0o644)
	got, ok := readZoneTab(tab)
	if !ok {
		t.Fatal("readZoneTab returned !ok")
	}
	want := []string{"America/New_York", "Europe/Bucharest"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("zones = %v, want %v", got, want)
	}
}

func TestValidateZone_Escapes(t *testing.T) {
	dir := t.TempDir()
	restore := swap(&zoneinfoDir, dir)
	defer restore()
	for _, bad := range []string{"", "/etc/passwd", "../../etc/passwd", "no-such-zone"} {
		if err := validateZone(bad); err == nil {
			t.Errorf("validateZone(%q) accepted, should reject", bad)
		}
	}
}

func TestValidateZone_Real(t *testing.T) {
	dir := t.TempDir()
	restore := swap(&zoneinfoDir, dir)
	defer restore()
	// Write a minimal TZif magic to a subdir.
	os.MkdirAll(filepath.Join(dir, "Europe"), 0o755)
	os.WriteFile(filepath.Join(dir, "Europe", "Bucharest"),
		[]byte("TZif2\x00\x00\x00\x00"), 0o644)
	if err := validateZone("Europe/Bucharest"); err != nil {
		t.Errorf("real zone rejected: %v", err)
	}
	if err := validateZone("Europe"); err == nil {
		t.Error("directory should not validate as zone")
	}
}

func TestCurrentZoneName(t *testing.T) {
	dir := t.TempDir()
	restore1 := swap(&zoneinfoDir, dir)
	defer restore1()
	restore2 := swap(&localtimeSym, filepath.Join(dir, "localtime"))
	defer restore2()

	os.MkdirAll(filepath.Join(dir, "Europe"), 0o755)
	os.WriteFile(filepath.Join(dir, "Europe", "Bucharest"),
		[]byte("TZif2\x00"), 0o644)
	os.Symlink(filepath.Join(dir, "Europe", "Bucharest"), localtimeSym)

	if got := currentZoneName(); got != "Europe/Bucharest" {
		t.Errorf("currentZoneName() = %q, want Europe/Bucharest", got)
	}
}

func TestAtomicSymlink(t *testing.T) {
	dir := t.TempDir()
	target1 := filepath.Join(dir, "target1")
	target2 := filepath.Join(dir, "target2")
	link := filepath.Join(dir, "link")
	os.WriteFile(target1, []byte("one"), 0o644)
	os.WriteFile(target2, []byte("two"), 0o644)

	if err := atomicSymlink(target1, link); err != nil {
		t.Fatalf("initial: %v", err)
	}
	got, _ := os.Readlink(link)
	if got != target1 {
		t.Errorf("first link → %q, want %q", got, target1)
	}
	// Replace: rename over existing symlink must succeed.
	if err := atomicSymlink(target2, link); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, _ = os.Readlink(link)
	if got != target2 {
		t.Errorf("replaced link → %q, want %q", got, target2)
	}
}

func TestRenderShow(t *testing.T) {
	s := statusFields{
		Timezone:              "Europe/Bucharest",
		LocalTime:             time.Unix(1700000000, 0),
		RTCTimeValid:          true,
		RTCTime:               time.Unix(1699999900, 0),
		RTCInLocalTZ:          true,
		CanNTP:                true,
		NTPService:            "chronyd",
		NTPServiceRunning:     true,
		SystemClockSynchronized: true,
	}
	var buf bytes.Buffer
	if err := renderShow(&buf, s); err != nil {
		t.Fatalf("renderShow: %v", err)
	}
	txt := buf.String()
	for _, want := range []string{
		"Timezone=Europe/Bucharest",
		"LocalRTC=yes",
		"NTP=yes",
		"NTPService=chronyd",
		"TimeUSec=1700000000000000",
	} {
		if !strings.Contains(txt, want) {
			t.Errorf("show output missing %q:\n%s", want, txt)
		}
	}
}

func TestParseRelative(t *testing.T) {
	cases := map[string]time.Duration{
		"+5min":  5 * time.Minute,
		"-30s":   -30 * time.Second,
		"+2h":    2 * time.Hour,
		"+1d":    24 * time.Hour,
		"+3days": 72 * time.Hour,
	}
	for in, want := range cases {
		got, err := parseRelative(in)
		if err != nil {
			t.Errorf("parseRelative(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseRelative(%q) = %v, want %v", in, got, want)
		}
	}
}

func swap(p *string, v string) func() {
	old := *p
	*p = v
	return func() { *p = old }
}
