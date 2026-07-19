package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCountLines(t *testing.T) {
	dir := t.TempDir()
	// Non-empty and blank lines mixed; only non-blank should count.
	path := filepath.Join(dir, "procs")
	if err := os.WriteFile(path, []byte("101\n\n202\n303\n\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := countLines(path); got != 3 {
		t.Errorf("countLines: got %d, want 3", got)
	}
	if got := countLines(filepath.Join(dir, "missing")); got != 0 {
		t.Errorf("countLines(missing): got %d, want 0", got)
	}
}

func TestReadKVAndUint(t *testing.T) {
	dir := t.TempDir()

	statPath := filepath.Join(dir, "cpu.stat")
	os.WriteFile(statPath, []byte("usage_usec 123456\nuser_usec 100000\n"), 0644)
	if got := readKV(statPath, "usage_usec"); got != 123456 {
		t.Errorf("readKV usage_usec: got %d, want 123456", got)
	}
	if got := readKV(statPath, "nonexistent"); got != 0 {
		t.Errorf("readKV nonexistent: got %d, want 0", got)
	}

	memPath := filepath.Join(dir, "memory.current")
	os.WriteFile(memPath, []byte("987654321\n"), 0644)
	if got := readUint(memPath); got != 987654321 {
		t.Errorf("readUint: got %d, want 987654321", got)
	}

	// "max" — unbounded cgroups collapse to 0 for display.
	maxPath := filepath.Join(dir, "memory.max")
	os.WriteFile(maxPath, []byte("max\n"), 0644)
	if got := readUint(maxPath); got != 0 {
		t.Errorf("readUint(max): got %d, want 0", got)
	}
}

func TestReadIOStat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "io.stat")
	// Two devices; sum should be per-column.
	os.WriteFile(path, []byte(
		"8:0 rbytes=1000 wbytes=2000 rios=1 wios=1\n"+
			"8:16 rbytes=500 wbytes=750 rios=1 wios=1\n"), 0644)
	rb, wb := readIOStat(path)
	if rb != 1500 || wb != 2750 {
		t.Errorf("readIOStat: got (%d, %d), want (1500, 2750)", rb, wb)
	}
}

// TestDiffRateComputation walks the diff() rate formula for CPU% and
// IO bytes/s. Uses a synthesised sample pair so the numbers are
// deterministic regardless of host activity.
func TestDiffRateComputation(t *testing.T) {
	prev := map[string]sample{
		"a": {path: "a", cpuUsageUS: 1_000_000, ioRead: 100, ioWrite: 200},
	}
	curr := map[string]sample{
		// After 1s of wall-clock: consumed 500ms of CPU-time on one
		// core (500_000us delta), 1KiB read, 2KiB written.
		"a": {path: "a", tasks: 1, cpuUsageUS: 1_500_000, ioRead: 1124, ioWrite: 2248, memCurrent: 4096},
	}
	rows := diff(prev, curr, time.Second, false)
	if len(rows) != 1 {
		t.Fatalf("diff: got %d rows, want 1", len(rows))
	}
	got := rows[0]
	if got.cpuPct < 49.9 || got.cpuPct > 50.1 {
		t.Errorf("cpuPct: got %f, want ~50.0", got.cpuPct)
	}
	if got.ioReadRate != 1024 {
		t.Errorf("ioReadRate: got %d, want 1024", got.ioReadRate)
	}
	if got.ioWriteRate != 2048 {
		t.Errorf("ioWriteRate: got %d, want 2048", got.ioWriteRate)
	}
	if got.mem != 4096 {
		t.Errorf("mem: got %d, want 4096", got.mem)
	}
}

// TestDiffFiltersIdleCgroups guards the --all-off default: cgroups
// with zero tasks, zero memory, and zero CPU% are omitted.
func TestDiffFiltersIdleCgroups(t *testing.T) {
	prev := map[string]sample{"idle": {path: "idle"}}
	curr := map[string]sample{"idle": {path: "idle"}}
	rows := diff(prev, curr, time.Second, false)
	if len(rows) != 0 {
		t.Errorf("idle cgroup should be filtered out; got %d rows", len(rows))
	}
	rows = diff(prev, curr, time.Second, true) // --all keeps everything
	if len(rows) != 1 {
		t.Errorf("--all should include idle cgroup; got %d rows", len(rows))
	}
}

func TestHumanBytes(t *testing.T) {
	for _, tc := range []struct {
		in   uint64
		want string
	}{
		{0, "-"},
		{512, "512B"},
		{2048, "2.0K"},
		{1024 * 1024, "1.0M"},
		{5 * 1024 * 1024 * 1024, "5.0G"},
	} {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d): got %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDepthOf(t *testing.T) {
	for _, tc := range []struct {
		rel  string
		want int
	}{
		{"", 0},
		{"system.slice", 1},
		{"system.slice/nginx.service", 2},
		{"a/b/c/d", 4},
	} {
		if got := depthOf(tc.rel); got != tc.want {
			t.Errorf("depthOf(%q): got %d, want %d", tc.rel, got, tc.want)
		}
	}
}
