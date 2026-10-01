package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Configuration for the button and lid handlers.
//
// Until now every Handle*Key and HandleLidSwitch property returned
// "ignore", and that was the honest answer: there was no watcher, so
// claiming "suspend" would have been a lie. With a watcher there is
// something to configure, and the properties report what is configured.
//
// The file is systemd's logind.conf format — an INI with a [Login]
// section — because that is what the configuration being replaced looks
// like. A Sunlight box migrating off elogind already has one, so
// /etc/elogind/logind.conf is read when slinit's own file is absent
// rather than making the operator copy it across.

// buttonAction is what to do when a button is pressed or a lid shuts.
type buttonAction string

const (
	actionIgnore               buttonAction = "ignore"
	actionPoweroff             buttonAction = "poweroff"
	actionReboot               buttonAction = "reboot"
	actionHalt                 buttonAction = "halt"
	actionSuspend              buttonAction = "suspend"
	actionHibernate            buttonAction = "hibernate"
	actionHybridSleep          buttonAction = "hybrid-sleep"
	actionSuspendThenHibernate buttonAction = "suspend-then-hibernate"
	actionLock                 buttonAction = "lock"
)

// knownActions is the set accepted from the config file. An unknown
// value is reported and falls back to "ignore": a typo that silently
// became "poweroff" would be the worst possible failure, and one that
// silently became "ignore" without saying so would leave an operator
// wondering why their lid does nothing.
var knownActions = map[buttonAction]struct{}{
	actionIgnore: {}, actionPoweroff: {}, actionReboot: {}, actionHalt: {},
	actionSuspend: {}, actionHibernate: {}, actionHybridSleep: {},
	actionSuspendThenHibernate: {}, actionLock: {},
}

// buttonConfig holds the handler settings. Zero value is every handler
// off, which is what a machine with no config file gets — the behaviour
// before this existed.
type buttonConfig struct {
	PowerKey     buttonAction
	SuspendKey   buttonAction
	HibernateKey buttonAction

	LidSwitch              buttonAction
	LidSwitchExternalPower buttonAction
	LidSwitchDocked        buttonAction

	// HoldoffTimeout suppresses lid events for this long after start-up
	// and after each wake. Without it a machine whose lid switch reports
	// "closed" as it resumes suspends again immediately, which reads to
	// the operator as a laptop that will not wake up.
	HoldoffTimeout time.Duration

	// InhibitDelayMax caps the wait for delay locks. Config can shorten
	// or lengthen the 5s default.
	InhibitDelayMax time.Duration

	// explicitExternalPower records whether the file set
	// HandleLidSwitchExternalPower, so an operator who wrote "ignore"
	// there on purpose is not overridden by the rule that otherwise
	// makes it follow HandleLidSwitch.
	explicitExternalPower bool

	// Warnings collects what was wrong with the file so the caller can
	// log it. Parsing never fails: a daemon that refuses to start
	// because of one bad line takes the desktop with it.
	Warnings []string
}

// defaultButtonConfig returns the settings used when no file is found.
//
// Deliberately all-"ignore", which is NOT systemd's default (it powers
// off on the power key and suspends on lid close). Turning those on for
// every existing installation the moment this daemon gains a watcher
// would change what the hardware does on upgrade, with no file edited to
// ask for it. An operator opts in.
func defaultButtonConfig() buttonConfig {
	return buttonConfig{
		PowerKey: actionIgnore, SuspendKey: actionIgnore,
		HibernateKey: actionIgnore, LidSwitch: actionIgnore,
		LidSwitchExternalPower: actionIgnore, LidSwitchDocked: actionIgnore,
		HoldoffTimeout:  30 * time.Second,
		InhibitDelayMax: inhibitDelayMax,
	}
}

// buttonConfigPaths is searched in order, first existing file wins.
var buttonConfigPaths = []string{
	"/etc/slinit/logind.conf",
	"/etc/elogind/logind.conf",
}

// loadButtonConfig reads the first config file that exists. A missing
// file is not an error — it means the defaults, which is every handler
// off.
func loadButtonConfig() buttonConfig {
	cfg := defaultButtonConfig()
	for _, path := range buttonConfigPaths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		cfg.parse(string(b), path)
		break
	}
	return cfg
}

// parse reads the [Login] section. Keys outside it are ignored, as are
// unknown keys inside it — logind.conf carries plenty this daemon has no
// opinion on, and warning about each would bury the warnings that matter.
func (c *buttonConfig) parse(text, path string) {
	inLogin := false
	for n, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inLogin = strings.EqualFold(line, "[Login]")
			continue
		}
		if !inLogin {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		where := fmt.Sprintf("%s:%d", path, n+1)
		switch key {
		case "HandlePowerKey":
			c.PowerKey = c.action(value, key, where)
		case "HandleSuspendKey":
			c.SuspendKey = c.action(value, key, where)
		case "HandleHibernateKey":
			c.HibernateKey = c.action(value, key, where)
		case "HandleLidSwitch":
			c.LidSwitch = c.action(value, key, where)
		case "HandleLidSwitchExternalPower":
			c.LidSwitchExternalPower = c.action(value, key, where)
		case "HandleLidSwitchDocked":
			c.LidSwitchDocked = c.action(value, key, where)
		case "HoldoffTimeoutSec":
			c.HoldoffTimeout = c.seconds(value, key, where, c.HoldoffTimeout)
		case "InhibitDelayMaxSec":
			c.InhibitDelayMax = c.seconds(value, key, where, c.InhibitDelayMax)
		}
	}

	// systemd's rule: an unset HandleLidSwitchExternalPower follows
	// HandleLidSwitch, so configuring the plain one is enough for a
	// machine that behaves the same on battery and mains. Applied after
	// the whole file is read, so key order does not matter.
	if c.LidSwitchExternalPower == actionIgnore && c.LidSwitch != actionIgnore &&
		!c.explicitExternalPower {
		c.LidSwitchExternalPower = c.LidSwitch
	}
}

func (c *buttonConfig) action(value, key, where string) buttonAction {
	if key == "HandleLidSwitchExternalPower" {
		c.explicitExternalPower = true
	}
	a := buttonAction(strings.ToLower(value))
	if _, ok := knownActions[a]; !ok {
		c.Warnings = append(c.Warnings, fmt.Sprintf(
			"%s: %s=%q is not a known action; treating as ignore", where, key, value))
		return actionIgnore
	}
	return a
}

// seconds accepts a bare number of seconds, systemd's shorthand for this
// family of keys, and also a Go duration so "500ms" is not a parse
// error for someone who tries it.
func (c *buttonConfig) seconds(value, key, where string, fallback time.Duration) time.Duration {
	if n, err := strconv.Atoi(value); err == nil {
		if n < 0 {
			c.Warnings = append(c.Warnings, fmt.Sprintf(
				"%s: %s=%q is negative; keeping %s", where, key, value, fallback))
			return fallback
		}
		return time.Duration(n) * time.Second
	}
	if d, err := time.ParseDuration(value); err == nil && d >= 0 {
		return d
	}
	c.Warnings = append(c.Warnings, fmt.Sprintf(
		"%s: %s=%q is not a number of seconds; keeping %s", where, key, value, fallback))
	return fallback
}

// lidAction picks which of the three lid settings applies right now.
// Docked wins over external power, matching systemd: a docked laptop
// with the lid shut is a desktop.
func (c *buttonConfig) lidAction(docked, externalPower bool) buttonAction {
	switch {
	case docked:
		return c.LidSwitchDocked
	case externalPower:
		return c.LidSwitchExternalPower
	default:
		return c.LidSwitch
	}
}
