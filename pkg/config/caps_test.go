package config

import (
	"strings"
	"testing"
)

// A typo in a capability or securebit name used to drop the whole
// setting at load time without a word: the service ran without the
// ambient caps, the bounding set or the securebits its file declared.
func TestCapabilitySettingsRejectUnknownNames(t *testing.T) {
	for _, line := range []string{
		"capabilities = cap_net_bind_service cap_net_bind_servce",
		"capability-bounding-set = cap_chown,cap_nosuch",
		"securebits = noroot keep-capz",
	} {
		_, err := Parse(strings.NewReader("type = process\ncommand = /bin/true\n"+line+"\n"),
			"svc", "svc")
		if err == nil {
			t.Errorf("%q: parsed without error", line)
			continue
		}
		key := strings.Fields(line)[0]
		if !strings.Contains(err.Error(), key) {
			t.Errorf("%q: error does not name the setting: %v", line, err)
		}
	}
}

// += appends, as for every other list setting; it used to replace.
func TestCapabilitySettingsPlusEqualAppends(t *testing.T) {
	desc, err := Parse(strings.NewReader(`type = process
command = /bin/true
capabilities = cap_chown
capabilities += cap_net_bind_service
capability-bounding-set = cap_chown
capability-bounding-set += cap_kill
securebits = noroot
securebits += keep-caps
`), "svc", "svc")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(desc.Capabilities); len(got) != 2 {
		t.Errorf("capabilities = %q, want both entries", desc.Capabilities)
	}
	if got := strings.Fields(desc.CapabilityBoundingSet); len(got) != 2 {
		t.Errorf("capability-bounding-set = %q, want both entries", desc.CapabilityBoundingSet)
	}
	if got := strings.Fields(desc.Securebits); len(got) != 2 {
		t.Errorf("securebits = %q, want both entries", desc.Securebits)
	}
}

// Commas separate securebits too, as they do capabilities.
func TestSecurebitsAcceptsCommas(t *testing.T) {
	if _, err := Parse(strings.NewReader("type = process\ncommand = /bin/true\nsecurebits = keep-caps,no-setuid-fixup\n"),
		"svc", "svc"); err != nil {
		t.Errorf("comma-separated securebits refused: %v", err)
	}
}
