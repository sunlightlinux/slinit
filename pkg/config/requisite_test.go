package config

import (
	"strings"
	"testing"
)

// Requires= and Requisite= shared one case arm and both became
// depends-on. They are not the same directive: Requires=B starts B
// alongside the unit, while Requisite=B says B must ALREADY be active,
// must not be started by this unit, and the unit fails if it is not.
// Mapped to depends-on, a unit written to refuse to start without a
// precondition instead pulled the precondition up and succeeded — the
// opposite of what it asked for, and with no warning, unlike Before=,
// Conflicts= and OnFailure= which all say something.
func TestRequisiteIsNotTranslatedAsRequires(t *testing.T) {
	cfg := &SystemdConfig{}
	warns := parseSystemdUnit(cfg, "[Unit]\nRequisite=db.service\n[Service]\nExecStart=/x\n")

	for _, d := range cfg.depends {
		if strings.Contains(d, "db") {
			t.Errorf("Requisite= still became a depends-on (%q) — that starts the "+
				"dependency, which is exactly what Requisite= forbids", d)
		}
	}

	var found bool
	for _, c := range cfg.conditions {
		if c.name == "assert-service-started" && c.value == "db" {
			found = true
		}
	}
	if !found {
		t.Errorf("Requisite= did not become assert-service-started; conditions = %+v",
			cfg.conditions)
	}

	// The ordering caveat is the operator's to act on, so it must be said.
	var noted bool
	for _, w := range warns {
		if strings.Contains(w.Msg, "Requisite") && strings.Contains(w.Msg, "after:") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("no note about pairing with after:; warnings = %+v", warns)
	}
}

// Requires= keeps its own meaning.
func TestRequiresStillBecomesDependsOn(t *testing.T) {
	cfg := &SystemdConfig{}
	parseSystemdUnit(cfg, "[Unit]\nRequires=db.service\n[Service]\nExecStart=/x\n")

	var found bool
	for _, d := range cfg.depends {
		if strings.Contains(d, "db") {
			found = true
		}
	}
	if !found {
		t.Errorf("Requires= no longer maps to depends-on; depends = %+v", cfg.depends)
	}
	for _, c := range cfg.conditions {
		if c.name == "assert-service-started" {
			t.Errorf("Requires= became a precondition; it must start the dependency")
		}
	}
}
