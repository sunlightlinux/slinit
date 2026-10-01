package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A machine with no config file must behave exactly as it did before the
// watcher existed: every handler off. Enabling them on upgrade would
// change what the hardware does with nothing having asked for it — a
// laptop that starts suspending on lid close after a package update.
func TestNoConfigMeansEveryHandlerOff(t *testing.T) {
	prev := buttonConfigPaths
	buttonConfigPaths = []string{filepath.Join(t.TempDir(), "absent.conf")}
	t.Cleanup(func() { buttonConfigPaths = prev })

	cfg := loadButtonConfig()
	for name, got := range map[string]buttonAction{
		"HandlePowerKey":               cfg.PowerKey,
		"HandleSuspendKey":             cfg.SuspendKey,
		"HandleHibernateKey":           cfg.HibernateKey,
		"HandleLidSwitch":              cfg.LidSwitch,
		"HandleLidSwitchExternalPower": cfg.LidSwitchExternalPower,
		"HandleLidSwitchDocked":        cfg.LidSwitchDocked,
	} {
		if got != actionIgnore {
			t.Errorf("%s defaults to %q, want ignore", name, got)
		}
	}
	if newButtonWatcher(nil, cfg).enabled() {
		t.Error("watcher should be disabled with nothing configured")
	}
}

func writeConf(t *testing.T, body string) buttonConfig {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "logind.conf")
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	prev := buttonConfigPaths
	buttonConfigPaths = []string{p}
	t.Cleanup(func() { buttonConfigPaths = prev })
	return loadButtonConfig()
}

func TestParsesLoginSection(t *testing.T) {
	cfg := writeConf(t, `
# a comment
[Sleep]
HandleLidSwitch=poweroff

[Login]
HandlePowerKey=poweroff
HandleSuspendKey=suspend
HandleHibernateKey=hibernate
HandleLidSwitch=suspend
HandleLidSwitchDocked=ignore
HoldoffTimeoutSec=12
InhibitDelayMaxSec=8
`)
	if cfg.PowerKey != actionPoweroff {
		t.Errorf("PowerKey = %q", cfg.PowerKey)
	}
	if cfg.SuspendKey != actionSuspend {
		t.Errorf("SuspendKey = %q", cfg.SuspendKey)
	}
	if cfg.HibernateKey != actionHibernate {
		t.Errorf("HibernateKey = %q", cfg.HibernateKey)
	}
	if cfg.LidSwitch != actionSuspend {
		t.Errorf("LidSwitch = %q", cfg.LidSwitch)
	}
	if cfg.HoldoffTimeout != 12*time.Second {
		t.Errorf("HoldoffTimeout = %s", cfg.HoldoffTimeout)
	}
	if cfg.InhibitDelayMax != 8*time.Second {
		t.Errorf("InhibitDelayMax = %s", cfg.InhibitDelayMax)
	}
	if len(cfg.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", cfg.Warnings)
	}
}

// A key outside [Login] must not take effect — the [Sleep] section above
// sets HandleLidSwitch=poweroff, and honouring it would power the
// machine off on a lid close the operator asked to suspend.
func TestKeysOutsideLoginSectionIgnored(t *testing.T) {
	cfg := writeConf(t, "[Sleep]\nHandlePowerKey=poweroff\n")
	if cfg.PowerKey != actionIgnore {
		t.Errorf("a key in [Sleep] leaked into the config: PowerKey = %q", cfg.PowerKey)
	}
}

// An unset HandleLidSwitchExternalPower follows HandleLidSwitch, as in
// systemd, so configuring one key covers a laptop that behaves the same
// on battery and mains.
func TestExternalPowerFollowsLidSwitchUnlessSet(t *testing.T) {
	cfg := writeConf(t, "[Login]\nHandleLidSwitch=suspend\n")
	if cfg.LidSwitchExternalPower != actionSuspend {
		t.Errorf("unset external-power should follow LidSwitch, got %q",
			cfg.LidSwitchExternalPower)
	}

	// But an explicit "ignore" is a decision, not an absence: a laptop
	// that should stay awake on mains with the lid shut.
	cfg = writeConf(t, "[Login]\nHandleLidSwitch=suspend\nHandleLidSwitchExternalPower=ignore\n")
	if cfg.LidSwitchExternalPower != actionIgnore {
		t.Errorf("an explicit ignore was overridden: got %q", cfg.LidSwitchExternalPower)
	}
}

// A typo must be reported and fall back to ignore. Falling back to
// anything else would be worse — silently powering off on a lid close
// because "suspned" did not parse is the nightmare case.
func TestUnknownActionWarnsAndIgnores(t *testing.T) {
	cfg := writeConf(t, "[Login]\nHandleLidSwitch=suspned\n")
	if cfg.LidSwitch != actionIgnore {
		t.Errorf("a bad action should become ignore, got %q", cfg.LidSwitch)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "suspned") {
		t.Errorf("expected a warning naming the bad value, got %v", cfg.Warnings)
	}
	// And it must name where, or an operator cannot find it.
	if !strings.Contains(cfg.Warnings[0], "logind.conf:2") {
		t.Errorf("warning should name file and line, got %q", cfg.Warnings[0])
	}
}

func TestBadTimeoutKeepsDefault(t *testing.T) {
	cfg := writeConf(t, "[Login]\nHoldoffTimeoutSec=soon\nInhibitDelayMaxSec=-5\n")
	if cfg.HoldoffTimeout != 30*time.Second {
		t.Errorf("bad holdoff should keep the default, got %s", cfg.HoldoffTimeout)
	}
	if cfg.InhibitDelayMax != inhibitDelayMax {
		t.Errorf("negative delay should keep the default, got %s", cfg.InhibitDelayMax)
	}
	if len(cfg.Warnings) != 2 {
		t.Errorf("expected a warning each, got %v", cfg.Warnings)
	}
	// A Go duration is accepted too, so someone who writes 500ms is not
	// told it is not a number.
	cfg = writeConf(t, "[Login]\nHoldoffTimeoutSec=1500ms\n")
	if cfg.HoldoffTimeout != 1500*time.Millisecond {
		t.Errorf("duration form not accepted, got %s", cfg.HoldoffTimeout)
	}
}

// Docked beats external power: a docked laptop with the lid shut is a
// desktop, whatever it is plugged into.
func TestLidActionPrecedence(t *testing.T) {
	cfg := buttonConfig{
		LidSwitch:              actionSuspend,
		LidSwitchExternalPower: actionLock,
		LidSwitchDocked:        actionIgnore,
	}
	if got := cfg.lidAction(true, true); got != actionIgnore {
		t.Errorf("docked+AC should use the docked setting, got %q", got)
	}
	if got := cfg.lidAction(true, false); got != actionIgnore {
		t.Errorf("docked on battery should still use the docked setting, got %q", got)
	}
	if got := cfg.lidAction(false, true); got != actionLock {
		t.Errorf("AC and undocked should use the external-power setting, got %q", got)
	}
	if got := cfg.lidAction(false, false); got != actionSuspend {
		t.Errorf("battery and undocked should use the plain setting, got %q", got)
	}
}

// The elogind path is read when slinit's own file is absent, so a box
// migrating off elogind keeps its configuration without the operator
// copying it across.
func TestFallsBackToElogindConfig(t *testing.T) {
	dir := t.TempDir()
	slinitConf := filepath.Join(dir, "slinit-logind.conf")
	elogindConf := filepath.Join(dir, "elogind-logind.conf")
	if err := os.WriteFile(elogindConf, []byte("[Login]\nHandleLidSwitch=hibernate\n"), 0644); err != nil {
		t.Fatal(err)
	}
	prev := buttonConfigPaths
	buttonConfigPaths = []string{slinitConf, elogindConf}
	t.Cleanup(func() { buttonConfigPaths = prev })

	if got := loadButtonConfig().LidSwitch; got != actionHibernate {
		t.Errorf("elogind config not used: LidSwitch = %q", got)
	}

	// And slinit's own file wins when both exist.
	if err := os.WriteFile(slinitConf, []byte("[Login]\nHandleLidSwitch=suspend\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := loadButtonConfig().LidSwitch; got != actionSuspend {
		t.Errorf("slinit's own config should win, got %q", got)
	}
}
