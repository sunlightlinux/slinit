package shutdown

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withSleepHook installs an executable hook and points the lookup at it,
// returning the path the hook appends its arguments to.
func withSleepHook(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	hook := filepath.Join(dir, "sleep-hook")
	log := filepath.Join(dir, "calls")
	body := "#!/bin/sh\n" + strings.ReplaceAll(script, "@LOG@", log) + "\n"
	if err := os.WriteFile(hook, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	prev := sleepHookPaths
	sleepHookPaths = []string{hook}
	t.Cleanup(func() { sleepHookPaths = prev })
	return log
}

// withFakePowerState points the sysfs write at a temp file that already
// advertises the state, so Suspend's support check passes and the write
// lands somewhere harmless.
func withFakePowerState(t *testing.T, supported string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(f, []byte(supported+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	prev := powerStatePath
	powerStatePath = f
	t.Cleanup(func() { powerStatePath = prev })
	return f
}

// The hook has to bracket the kernel write: "pre" before, "post" after.
// For suspend-to-RAM the post call is the first thing that runs after
// the machine wakes, which is the whole reason it exists.
func TestSleepHookBracketsTheWrite(t *testing.T) {
	state := withFakePowerState(t, "freeze mem disk")
	log := withSleepHook(t, `echo "$1 $2 $3" >> @LOG@; cat `+state+` >> @LOG@`)

	if err := Suspend("mem", nil); err != nil {
		t.Fatalf("Suspend: %v", err)
	}

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("hook never ran: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected two hook calls with a state read each, got %q", lines)
	}
	if lines[0] != "pre suspend mem" {
		t.Errorf("first call: got %q, want \"pre suspend mem\"", lines[0])
	}
	if lines[2] != "post suspend mem" {
		t.Errorf("second call: got %q, want \"post suspend mem\"", lines[2])
	}
	// The pre hook must see the file before the write, the post hook
	// after it — that is what proves the ordering rather than just the
	// argument strings.
	if strings.TrimSpace(lines[1]) == "mem" {
		t.Error("pre hook already saw the written state; it ran too late")
	}
	if strings.TrimSpace(lines[3]) != "mem" {
		t.Errorf("post hook did not see the written state, saw %q", lines[3])
	}
}

// Hibernate gets systemd's operation name, so a script lifted from
// /usr/lib/systemd/system-sleep/ branches correctly on $2.
func TestSleepHookOperationArgMatchesSystemd(t *testing.T) {
	withFakePowerState(t, "freeze mem disk")
	log := withSleepHook(t, `echo "$1 $2 $3" >> @LOG@`)

	if err := Suspend("disk", nil); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	got, _ := os.ReadFile(log)
	if !strings.Contains(string(got), "pre hibernate disk") {
		t.Errorf("hibernate should map to the hibernate operation, got %q", got)
	}

	// s2idle is still a suspend as far as a script is concerned, but the
	// raw kernel token is passed too for anything that cares.
	withFakePowerState(t, "freeze mem disk")
	log2 := withSleepHook(t, `echo "$1 $2 $3" >> @LOG@`)
	if err := Suspend("freeze", nil); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	got2, _ := os.ReadFile(log2)
	if !strings.Contains(string(got2), "pre suspend freeze") {
		t.Errorf("freeze should be operation=suspend state=freeze, got %q", got2)
	}
}

// A failing hook must not abort the suspend. A laptop whose lid is shut
// and which stays awake because a script exited non-zero overheats in a
// bag; systemd logs and continues, and so do we.
func TestSleepHookFailureDoesNotAbortSuspend(t *testing.T) {
	state := withFakePowerState(t, "mem")
	withSleepHook(t, `exit 3`)

	if err := Suspend("mem", nil); err != nil {
		t.Fatalf("a failing hook must not fail the suspend, got: %v", err)
	}
	data, _ := os.ReadFile(state)
	if strings.TrimSpace(string(data)) != "mem" {
		t.Errorf("the kernel write did not happen, file holds %q", data)
	}
}

// The post hook still runs when the write itself fails, so a pre hook
// that stopped something always gets its counterpart.
func TestSleepHookPostRunsAfterAFailedWrite(t *testing.T) {
	// A directory cannot be written to, which fails the write while
	// leaving the support check satisfied.
	dir := t.TempDir()
	prev := powerStatePath
	powerStatePath = dir
	t.Cleanup(func() { powerStatePath = prev })

	log := withSleepHook(t, `echo "$1" >> @LOG@`)
	// Reading a directory succeeds but yields no state list, so the
	// support check is skipped and the write is reached.
	if err := Suspend("mem", nil); err == nil {
		t.Fatal("expected the write to fail")
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("hook never ran: %v", err)
	}
	calls := strings.Fields(string(data))
	if len(calls) != 2 || calls[0] != "pre" || calls[1] != "post" {
		t.Errorf("expected pre then post even on failure, got %q", calls)
	}
}

// A non-executable hook is skipped rather than reported, matching the
// shutdown hook's rule.
func TestSleepHookIgnoredWhenNotExecutable(t *testing.T) {
	withFakePowerState(t, "mem")
	dir := t.TempDir()
	hook := filepath.Join(dir, "sleep-hook")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	prev := sleepHookPaths
	sleepHookPaths = []string{hook}
	t.Cleanup(func() { sleepHookPaths = prev })

	if err := Suspend("mem", nil); err != nil {
		t.Fatalf("Suspend should ignore a non-executable hook, got: %v", err)
	}
}
