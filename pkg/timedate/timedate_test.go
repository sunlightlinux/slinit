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

// slinitctl is-started exits 1 both for a stopped service and when it
// cannot reach slinit (the socket is root's); only the first prints a
// state. As a user, slinit-timedatectl said "chronyd (inactive)" on a
// NUC where chronyd was running and in sync.
func TestServiceStateTellsUnknownFromStopped(t *testing.T) {
	dir := t.TempDir()
	old := SlinitctlBin
	t.Cleanup(func() { SlinitctlBin = old })
	for _, c := range []struct {
		name, script   string
		running, known bool
	}{
		{"started", "echo STARTED; exit 0", true, true},
		{"stopped", "echo STOPPED; exit 1", false, true},
		{"no access", "echo 'slinitctl: Failed to connect to slinit at /run/slinit.socket: permission denied' >&2; exit 1", false, false},
	} {
		p := filepath.Join(dir, strings.ReplaceAll(c.name, " ", "-"))
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+c.script+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		SlinitctlBin = p
		running, known := ServiceState("chronyd")
		if running != c.running || known != c.known {
			t.Errorf("%s: ServiceState = (%v, %v), want (%v, %v)", c.name, running, known, c.running, c.known)
		}
	}
}

// TIMEZONE= is rewritten in place: an "export " before it, which the shell
// sourcing rc.conf acts on, and the line's indentation stay; a commented
// line and a variable that only starts with the name are not touched; a
// file without the assignment is left exactly as it was.
func TestSetRCConfVarKeepsTheLineShape(t *testing.T) {
	old := RCConfPath
	t.Cleanup(func() { RCConfPath = old })
	RCConfPath = filepath.Join(t.TempDir(), "rc.conf")

	in := "# rc.conf\n#TIMEZONE=\"UTC\"\nexport TIMEZONE=\"Europe/Bucharest\"\n" +
		"  TIMEZONE=Old/Zone\nTIMEZONE_EXTRA=keep\nKEYMAP=us\n"
	if err := os.WriteFile(RCConfPath, []byte(in), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := setRCConfVar("TIMEZONE", "Europe/Paris"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(RCConfPath)
	want := "# rc.conf\n#TIMEZONE=\"UTC\"\nexport TIMEZONE=\"Europe/Paris\"\n" +
		"  TIMEZONE=\"Europe/Paris\"\nTIMEZONE_EXTRA=keep\nKEYMAP=us\n"
	if string(b) != want {
		t.Errorf("rc.conf:\n%s\nwant:\n%s", b, want)
	}
	if st, _ := os.Stat(RCConfPath); st.Mode().Perm() != 0o640 {
		t.Errorf("mode %v, want the file's own 0640", st.Mode().Perm())
	}

	untouched := "KEYMAP=us\n#TIMEZONE=\"UTC\"\n"
	os.WriteFile(RCConfPath, []byte(untouched), 0o644)
	if err := setRCConfVar("TIMEZONE", "Europe/Paris"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(RCConfPath); string(b) != untouched {
		t.Errorf("a file without TIMEZONE= was changed:\n%s", b)
	}

	os.Remove(RCConfPath)
	if err := setRCConfVar("TIMEZONE", "Europe/Paris"); err != nil {
		t.Errorf("missing rc.conf: %v", err)
	}
	if _, err := os.Stat(RCConfPath); !os.IsNotExist(err) {
		t.Errorf("a missing rc.conf was created")
	}
}
