package config

import (
	"fmt"
	"strings"
)

// The directive registry of the .service loader.
//
// A systemd directive with a slinit counterpart is mapped through an
// sdRule; one slinit deliberately does without is listed in
// sdNoEquivalent with the reason, and one that means nothing outside
// systemd's own machinery in sdIgnored. The bespoke switch cases in
// applyServiceKey / applyUnitKey remain for directives whose mapping
// reaches beyond one slinit setting (Type=, the Exec*= family, the
// capability and syscall folds, dependencies).
//
// Before the registry, the switch's default case answered every
// directive it did not name with "not mapped". On a unit loaded
// directly that is a log line nobody reads, so MemoryMax=, TasksMax=,
// KillMode=, SuccessExitStatus=, RemainAfterExit= and about two hundred
// others were dropped although slinit implements most of them, under the
// same name. TestEverySystemdDirectiveIsClassified now checks every
// directive systemd defines (testdata/systemd-directives.txt) against
// these tables, so a directive the loader has no answer for fails the
// build instead of a service.

// sdClass says what a failed mapping costs.
type sdClass uint8

const (
	// sdBehaviour: a value slinit cannot take is dropped with a warning,
	// as systemd itself logs and ignores an invalid assignment.
	sdBehaviour sdClass = iota
	// sdConfine: the directive confines the service. A value slinit
	// cannot take refuses the unit rather than run it less confined than
	// it asks — the rule the capability and syscall folds already follow.
	sdConfine
)

// sdShape says how repeated assignments combine.
type sdShape uint8

const (
	// sdScalar: the last assignment wins, as for any systemd scalar.
	sdScalar sdShape = iota
	// sdList: assignments accumulate into one space-separated slinit
	// value; an empty assignment resets the list.
	sdList
)

// sdRule maps one systemd directive onto one slinit setting.
type sdRule struct {
	key   string // the slinit setting
	class sdClass
	shape sdShape
	// conv turns the systemd value into slinit's. nil passes the value
	// on as it is. An empty result with no error emits nothing (a value
	// that only restates the default); warnings ride along either way.
	conv func(val string) (string, []SystemdWarning, error)
	// custom replaces the whole mapping, for a directive that changes
	// more than one setting; key, class, shape and conv are then unused.
	custom func(cfg *SystemdConfig, key, val string) []SystemdWarning
}

// sdGap is a systemd directive with no slinit equivalent.
type sdGap struct {
	// confine marks a directive that confines the service: its absence
	// leaves the service less confined than the unit asks, which is a
	// WARN. Otherwise the gap is a NOTE.
	confine bool
	why     string
}

var (
	sdServiceRules = map[string]sdRule{}
	sdUnitRules    = map[string]sdRule{}

	sdServiceNoEquivalent = map[string]sdGap{}
	sdUnitNoEquivalent    = map[string]sdGap{}

	// sdIgnored are directives that only mean something inside systemd's
	// own machinery (D-Bus job modes, isolation, snapshot bookkeeping);
	// they are skipped without a word, as Documentation= always was.
	sdServiceIgnored = map[string]string{}
	sdUnitIgnored    = map[string]string{}
)

// sdFinishers run once the whole unit and its drop-ins are read, before
// the folds of finishSystemdUnit. A mapping whose meaning depends on
// other directives (Type=, a later reset, RootDirectory=, an implied
// setting a later line overrides) records what it saw in the unit's
// state (sdState) and decides here, so the order of lines and drop-ins
// does not change the result, as it does not in systemd.
var sdFinishers []func(cfg *SystemdConfig) []SystemdWarning

func registerFinisher(f func(cfg *SystemdConfig) []SystemdWarning) {
	sdFinishers = append(sdFinishers, f)
}

// sdState returns the unit's state under key, made by mk on first use.
// Each mapping file keeps its own typed struct under its own key.
func sdState[T any](cfg *SystemdConfig, key string, mk func() *T) *T {
	if cfg.state == nil {
		cfg.state = map[string]any{}
	}
	if v, ok := cfg.state[key]; ok {
		return v.(*T)
	}
	v := mk()
	cfg.state[key] = v
	return v
}

// hasMapped reports whether key has been emitted.
func (cfg *SystemdConfig) hasMapped(key string) (string, bool) {
	for _, kv := range cfg.mapped {
		if kv.key == key {
			return kv.value, true
		}
	}
	return "", false
}

// registerService / registerUnit add rules at package init. A directive
// registered twice is a programming error caught at start-up, not a
// silent override.
func registerService(sd string, r sdRule) { register(sdServiceRules, "Service", sd, r) }
func registerUnit(sd string, r sdRule)    { register(sdUnitRules, "Unit", sd, r) }

func register(m map[string]sdRule, section, sd string, r sdRule) {
	if _, dup := m[sd]; dup {
		panic(fmt.Sprintf("systemd registry: [%s] %s registered twice", section, sd))
	}
	if r.custom == nil && r.key == "" {
		panic(fmt.Sprintf("systemd registry: [%s] %s has neither a slinit key nor a custom mapping", section, sd))
	}
	m[sd] = r
}

// applyRule maps one assignment through r.
func (cfg *SystemdConfig) applyRule(section, key, val string, r sdRule) []SystemdWarning {
	if r.custom != nil {
		return r.custom(cfg, key, val)
	}
	v := strings.TrimSpace(trimQuotes(val))
	if r.shape == sdList && v == "" {
		cfg.dropMapped(r.key)
		return nil
	}
	out := v
	var warns []SystemdWarning
	if r.conv != nil {
		var err error
		out, warns, err = r.conv(v)
		if err != nil {
			return append(warns, cfg.ruleFailed(section, key, val, r, err)...)
		}
		if out == "" {
			return warns
		}
	}
	if err := validateSlinitSetting(r.key, out); err != nil {
		return append(warns, cfg.ruleFailed(section, key, val, r, fmt.Errorf("%s = %s: %w", r.key, out, err))...)
	}
	if r.shape == sdList {
		cfg.appendMapped(r.key, out)
	} else {
		cfg.setMapped(r.key, out)
	}
	return warns
}

// ruleFailed reports a value slinit cannot take: a confining directive
// refuses the unit, any other is dropped, as systemd drops it.
func (cfg *SystemdConfig) ruleFailed(section, key, val string, r sdRule, err error) []SystemdWarning {
	if r.class == sdConfine {
		cfg.errs = append(cfg.errs, fmt.Sprintf("%s=%s confines the service, and slinit cannot apply it (%v)", key, val, err))
		return nil
	}
	return []SystemdWarning{{"WARN", fmt.Sprintf("[%s] %s=%s ignored: %v", section, key, val, err)}}
}

// gapWarning reports a directive slinit does without. A value that only
// restates systemd's default (no, empty, 0) needs no word: the service
// gets in slinit what it would get in systemd.
func gapWarning(section, key, val string, g sdGap) []SystemdWarning {
	switch strings.ToLower(strings.TrimSpace(trimQuotes(val))) {
	case "", "no", "false", "off", "0":
		return nil
	}
	level := "NOTE"
	if g.confine {
		level = "WARN"
	}
	return []SystemdWarning{{level, fmt.Sprintf("[%s] %s=%s has no slinit equivalent: %s", section, key, val, g.why)}}
}

// validateSlinitSetting runs slinit's own parser over one setting, so a
// value the converter produces is one Parse accepts: the alternative is
// a unit that loaded before a mapping was added and fails after it.
func validateSlinitSetting(key, value string) error {
	d := NewServiceDescription("systemd-unit")
	return applySetting(d, key, value, OpEquals, nil)
}

// setMapped sets a scalar setting, replacing an earlier assignment.
func (cfg *SystemdConfig) setMapped(key, value string) {
	for i := range cfg.mapped {
		if cfg.mapped[i].key == key {
			cfg.mapped[i].value = value
			return
		}
	}
	cfg.mapped = append(cfg.mapped, kvPair{key, value})
}

// appendMapped adds to a list setting.
func (cfg *SystemdConfig) appendMapped(key, value string) {
	for i := range cfg.mapped {
		if cfg.mapped[i].key == key {
			cfg.mapped[i].value += " " + value
			return
		}
	}
	cfg.mapped = append(cfg.mapped, kvPair{key, value})
}

// dropMapped removes every assignment of key: an empty systemd list
// assignment resets the list.
func (cfg *SystemdConfig) dropMapped(key string) {
	kept := cfg.mapped[:0]
	for _, kv := range cfg.mapped {
		if kv.key != key {
			kept = append(kept, kv)
		}
	}
	cfg.mapped = kept
}
