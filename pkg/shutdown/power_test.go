package shutdown

import (
	"os"
	"path/filepath"
	"testing"
)

func withPowerStatusPaths(t *testing.T, paths ...string) {
	t.Helper()
	old := powerStatusPaths
	powerStatusPaths = paths
	t.Cleanup(func() { powerStatusPaths = old })
}

func withPowerHookPaths(t *testing.T, paths ...string) {
	t.Helper()
	old := powerHookPaths
	powerHookPaths = paths
	t.Cleanup(func() { powerHookPaths = old })
}

func TestReadPowerStatus(t *testing.T) {
	cases := []struct {
		name    string
		content string
		write   bool
		want    PowerState
	}{
		{"F is a power failure", "F", true, PowerFailing},
		{"O is power restored", "O", true, PowerOK},
		{"L is a low battery", "L", true, PowerLow},
		// Only the first byte is read, as sysvinit reads it, so a daemon
		// that writes a trailing newline is still understood.
		{"trailing newline", "O\n", true, PowerOK},
		{"lowercase is accepted too", "o", true, PowerOK},
		// Anything else means failing. sysvinit documents this, and it is
		// the safe direction: a needless hook run costs nothing, the
		// opposite costs the machine.
		{"an unknown letter", "X", true, PowerFailing},
		{"empty file", "", true, PowerFailing},
		{"no file at all", "", false, PowerFailing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "powerstatus")
			if tc.write {
				if err := os.WriteFile(path, []byte(tc.content), 0644); err != nil {
					t.Fatal(err)
				}
			}
			withPowerStatusPaths(t, path)
			if got := ReadPowerStatus(); got != tc.want {
				t.Errorf("ReadPowerStatus() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The status is an event, not a state: the daemon writes it and then
// signals. A file left behind would make the next SIGPWR replay a power
// failure that was already handled — so reading it consumes it.
func TestReadPowerStatusConsumesTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "powerstatus")
	if err := os.WriteFile(path, []byte("O"), 0644); err != nil {
		t.Fatal(err)
	}
	withPowerStatusPaths(t, path)

	if got := ReadPowerStatus(); got != PowerOK {
		t.Fatalf("first read = %q, want %q", got, PowerOK)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the status file survived the read: %v", err)
	}
	// And a second signal with nothing new to say reports failing rather
	// than repeating "ok".
	if got := ReadPowerStatus(); got != PowerFailing {
		t.Errorf("second read = %q, want %q", got, PowerFailing)
	}
}

// /run first, /etc last: sysvinit's manual documents /etc/powerstatus but
// its source has preferred /var/run since 2010 and warns about /etc.
func TestReadPowerStatusPrefersTheNewerPath(t *testing.T) {
	dir := t.TempDir()
	newer := filepath.Join(dir, "run-powerstatus")
	older := filepath.Join(dir, "etc-powerstatus")
	if err := os.WriteFile(newer, []byte("O"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(older, []byte("L"), 0644); err != nil {
		t.Fatal(err)
	}
	withPowerStatusPaths(t, newer, older)

	if got := ReadPowerStatus(); got != PowerOK {
		t.Errorf("ReadPowerStatus() = %q, want %q from the newer path", got, PowerOK)
	}
	// The one that was not consulted is left alone.
	if _, err := os.Stat(older); err != nil {
		t.Errorf("the fallback path was removed without being read: %v", err)
	}
}

func TestRunPowerHookPassesTheState(t *testing.T) {
	dir := t.TempDir()
	hook := filepath.Join(dir, "power-hook")
	out := filepath.Join(dir, "saw")
	script := "#!/bin/sh\nprintf '%s' \"$1\" > " + out + "\n"
	if err := os.WriteFile(hook, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	withPowerHookPaths(t, hook)

	RunPowerHook(PowerLow, nil)

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the hook did not run: %v", err)
	}
	if string(got) != "low" {
		t.Errorf("hook got %q, want %q", got, "low")
	}
}

// A hook that is absent, or present but not executable, must be a no-op
// rather than an error — the same contract as the shutdown and sleep
// hooks, so an operator can drop a non-executable template in place
// without arming it.
func TestRunPowerHookIgnoresWhatItCannotRun(t *testing.T) {
	dir := t.TempDir()

	withPowerHookPaths(t, filepath.Join(dir, "absent"))
	RunPowerHook(PowerFailing, nil) // must not panic

	notExec := filepath.Join(dir, "power-hook")
	if err := os.WriteFile(notExec, []byte("#!/bin/sh\nexit 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	withPowerHookPaths(t, notExec)
	RunPowerHook(PowerFailing, nil) // must not panic
}

// A hook that fails must not propagate: by the time it runs the power
// event has already happened and there is nothing to abort.
func TestRunPowerHookSurvivesAFailingHook(t *testing.T) {
	dir := t.TempDir()
	hook := filepath.Join(dir, "power-hook")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 7\n"), 0755); err != nil {
		t.Fatal(err)
	}
	withPowerHookPaths(t, hook)
	RunPowerHook(PowerLow, nil) // must return normally
}

func TestHandlePowerSignalReadsThenRuns(t *testing.T) {
	dir := t.TempDir()
	status := filepath.Join(dir, "powerstatus")
	if err := os.WriteFile(status, []byte("L"), 0644); err != nil {
		t.Fatal(err)
	}
	withPowerStatusPaths(t, status)

	hook := filepath.Join(dir, "power-hook")
	out := filepath.Join(dir, "saw")
	script := "#!/bin/sh\nprintf '%s' \"$1\" > " + out + "\n"
	if err := os.WriteFile(hook, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	withPowerHookPaths(t, hook)

	if got := HandlePowerSignal(nil); got != PowerLow {
		t.Errorf("HandlePowerSignal() = %q, want %q", got, PowerLow)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the hook did not run: %v", err)
	}
	if string(got) != "low" {
		t.Errorf("hook got %q, want %q", got, "low")
	}
}
