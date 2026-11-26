package shutdown

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
)

func clockTestLogger() *logging.Logger {
	return logging.New(logging.LevelDebug)
}

// clockScratch points the timestamp file into a temporary directory and
// replaces settimeofday with a recorder. These tests used to copy the
// parsing logic instead of calling it, and two of them read — or, run as
// root, wrote — the host's own /var/lib/slinit/clock.
func clockScratch(t *testing.T) (path string, set *[]time.Time) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "var/lib/slinit/clock")
	oldPath, oldSet := clockTimestampPath, settimeofdayFunc
	clockTimestampPath = path
	var calls []time.Time
	settimeofdayFunc = func(tt time.Time) error { calls = append(calls, tt); return nil }
	t.Cleanup(func() { clockTimestampPath, settimeofdayFunc = oldPath, oldSet })
	return path, &calls
}

func TestWriteAndReadClockTimestamp(t *testing.T) {
	path, _ := clockScratch(t)
	before := time.Now().Unix()
	// The directory does not exist yet: the write creates it.
	if err := WriteClockTimestamp(); err != nil {
		t.Fatal(err)
	}
	ts, err := readClockTimestamp()
	if err != nil {
		t.Fatal(err)
	}
	if ts.Unix() < before || ts.Unix() > time.Now().Unix() {
		t.Errorf("read back %d, written around %d", ts.Unix(), before)
	}
	// Atomic: no temporary file left beside it.
	ents, _ := os.ReadDir(filepath.Dir(path))
	if len(ents) != 1 || ents[0].Name() != "clock" {
		t.Errorf("directory holds %v", ents)
	}
}

func TestReadClockTimestampInvalid(t *testing.T) {
	path, _ := clockScratch(t)
	if _, err := readClockTimestamp(); err == nil {
		t.Errorf("missing file read without error")
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	for name, content := range map[string]string{
		"empty": "", "whitespace": "   \n", "not_a_number": "hello",
		"negative": "-1", "zero": "0", "float": "1234.567",
	} {
		os.WriteFile(path, []byte(content), 0o644)
		if ts, err := readClockTimestamp(); err == nil {
			t.Errorf("%s: read %v without error", name, ts)
		}
	}
	os.WriteFile(path, []byte("  1800000000 \n"), 0o644)
	if ts, err := readClockTimestamp(); err != nil || ts.Unix() != 1800000000 {
		t.Errorf("padded valid timestamp: %v %v", ts, err)
	}
}

// A clock ahead of both the floor and the file is left alone.
func TestClockGuardLeavesAGoodClock(t *testing.T) {
	path, set := clockScratch(t)
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)), 0o644)
	if d := ClockGuard(clockTestLogger()); d != 0 || len(*set) != 0 {
		t.Errorf("good clock adjusted: delta %v, set %v", d, *set)
	}
	// No file at all: the floor alone, which a real clock is past.
	os.Remove(path)
	if d := ClockGuard(clockTestLogger()); d != 0 || len(*set) != 0 {
		t.Errorf("no file: delta %v, set %v", d, *set)
	}
}

// A clock behind the last saved time is advanced to it, and the file is
// rewritten. A file older than the floor is outranked by the floor.
func TestClockGuardAdvancesToTheSavedTime(t *testing.T) {
	path, set := clockScratch(t)
	os.MkdirAll(filepath.Dir(path), 0o755)
	future := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	os.WriteFile(path, []byte(strconv.FormatInt(future.Unix(), 10)+"\n"), 0o644)

	d := ClockGuard(clockTestLogger())
	if len(*set) != 1 || !(*set)[0].Equal(future) {
		t.Fatalf("clock set to %v, want %v", *set, future)
	}
	if d < 47*time.Hour || d > 49*time.Hour {
		t.Errorf("delta %v, want about 48h", d)
	}
	if b, _ := os.ReadFile(path); string(b) == strconv.FormatInt(future.Unix(), 10)+"\n" {
		t.Errorf("timestamp file not rewritten after the correction")
	}

	// The floor wins over an older file.
	oldFloor := clockFloor
	t.Cleanup(func() { clockFloor = oldFloor })
	clockFloor = time.Now().Add(24 * time.Hour).Truncate(time.Second)
	os.WriteFile(path, []byte("1000000000\n"), 0o644)
	*set = nil
	ClockGuard(clockTestLogger())
	if len(*set) != 1 || !(*set)[0].Equal(clockFloor) {
		t.Errorf("clock set to %v, want the floor %v", *set, clockFloor)
	}
}

// A failed settimeofday reports no adjustment.
func TestClockGuardSetFailure(t *testing.T) {
	path, _ := clockScratch(t)
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)), 0o644)
	settimeofdayFunc = func(time.Time) error { return os.ErrPermission }
	if d := ClockGuard(clockTestLogger()); d != 0 {
		t.Errorf("failed set reported delta %v", d)
	}
}

// TestClockFloor verifies the floor is set to a reasonable date.
func TestClockFloor(t *testing.T) {
	if clockFloor.Year() != clockFloorYear {
		t.Errorf("clockFloor year = %d, want %d", clockFloor.Year(), clockFloorYear)
	}
	if clockFloor.After(time.Now()) {
		t.Errorf("clockFloor %v is in the future", clockFloor)
	}
}
