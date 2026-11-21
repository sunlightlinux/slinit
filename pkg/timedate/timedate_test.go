package timedate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdjtime_ReadMissing(t *testing.T) {
	restore := swap(&AdjtimePath, filepath.Join(t.TempDir(), "missing"))
	defer restore()
	mode, err := ReadAdjtimeMode()
	if err != nil {
		t.Fatalf("ReadAdjtimeMode: %v", err)
	}
	if mode != "UTC" {
		t.Errorf("missing adjtime → %q, want UTC (kernel default)", mode)
	}
	if RTCInLocal() {
		t.Error("RTCInLocal() should be false when adjtime missing")
	}
}

func TestAdjtime_ReadWriteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "adjtime")
	restore := swap(&AdjtimePath, path)
	defer restore()

	os.WriteFile(path, []byte("0.125000 100000 0.500000\n50000\nUTC\n"), 0o644)
	if mode, _ := ReadAdjtimeMode(); mode != "UTC" {
		t.Errorf("initial mode = %q, want UTC", mode)
	}
	if err := WriteAdjtimeMode("LOCAL"); err != nil {
		t.Fatalf("WriteAdjtimeMode: %v", err)
	}
	body, _ := os.ReadFile(path)
	text := string(body)
	if !strings.Contains(text, "0.125000 100000 0.500000") ||
		!strings.Contains(text, "\n50000\n") ||
		!strings.Contains(text, "\nLOCAL\n") {
		t.Errorf("adjtime after rewrite missing preserved fields:\n%s", text)
	}
	if mode, _ := ReadAdjtimeMode(); mode != "LOCAL" {
		t.Errorf("re-read mode = %q, want LOCAL", mode)
	}
}

func TestAdjtime_WriteMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "adjtime")
	restore := swap(&AdjtimePath, path)
	defer restore()
	if err := WriteAdjtimeMode("LOCAL"); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "0.000000") || !strings.Contains(string(got), "LOCAL") {
		t.Errorf("fresh adjtime unexpected:\n%s", got)
	}
}

func TestAdjtime_WriteBogusRejected(t *testing.T) {
	if err := WriteAdjtimeMode("wat"); err == nil {
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
	restore := swap(&ZoneinfoDir, dir)
	defer restore()
	for _, bad := range []string{"", "/etc/passwd", "../../etc/passwd", "no-such-zone"} {
		if err := ValidateZone(bad); err == nil {
			t.Errorf("ValidateZone(%q) accepted, should reject", bad)
		}
	}
}

func TestValidateZone_Real(t *testing.T) {
	dir := t.TempDir()
	restore := swap(&ZoneinfoDir, dir)
	defer restore()
	// Write a minimal TZif magic to a subdir.
	os.MkdirAll(filepath.Join(dir, "Europe"), 0o755)
	os.WriteFile(filepath.Join(dir, "Europe", "Bucharest"),
		[]byte("TZif2\x00\x00\x00\x00"), 0o644)
	if err := ValidateZone("Europe/Bucharest"); err != nil {
		t.Errorf("real zone rejected: %v", err)
	}
	if err := ValidateZone("Europe"); err == nil {
		t.Error("directory should not validate as zone")
	}
}

func TestCurrentZoneName(t *testing.T) {
	dir := t.TempDir()
	restore1 := swap(&ZoneinfoDir, dir)
	defer restore1()
	restore2 := swap(&LocaltimePath, filepath.Join(dir, "localtime"))
	defer restore2()

	os.MkdirAll(filepath.Join(dir, "Europe"), 0o755)
	os.WriteFile(filepath.Join(dir, "Europe", "Bucharest"),
		[]byte("TZif2\x00"), 0o644)
	os.Symlink(filepath.Join(dir, "Europe", "Bucharest"), LocaltimePath)

	if got := CurrentZone(); got != "Europe/Bucharest" {
		t.Errorf("CurrentZone() = %q, want Europe/Bucharest", got)
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

func swap(p *string, v string) func() {
	old := *p
	*p = v
	return func() { *p = old }
}

// A time-sync service a package ships lives in /lib/slinit.d, not
// /etc/slinit.d. Only the latter used to be searched, so on Sunlight
// chronyd was never found and CanNTP read false while it ran.
func TestFindNTPServiceSearchesEveryServiceDir(t *testing.T) {
	etc, lib := t.TempDir(), t.TempDir()
	old := SlinitServiceDirs
	SlinitServiceDirs = []string{etc, lib}
	t.Cleanup(func() { SlinitServiceDirs = old })

	if got := FindNTPService(); got != "" {
		t.Fatalf("empty dirs: %q", got)
	}
	if err := os.WriteFile(filepath.Join(lib, "chronyd"), []byte("type = process\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := FindNTPService(); got != "chronyd" {
		t.Errorf("chronyd in a package directory: FindNTPService = %q", got)
	}
	// Preference order still holds across directories: chronyd wins over
	// an ntpd found earlier in the search path.
	if err := os.WriteFile(filepath.Join(etc, "ntpd"), []byte("type = process\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := FindNTPService(); got != "chronyd" {
		t.Errorf("preference order: FindNTPService = %q, want chronyd", got)
	}
}
