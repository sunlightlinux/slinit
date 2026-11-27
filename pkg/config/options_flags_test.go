package config

import (
	"strings"
	"testing"
)

// options = replaces what options sets, and only that.
func TestOptionsKeepsNoBootMarker(t *testing.T) {
	d, err := Parse(strings.NewReader("type = internal\nno-boot-marker = yes\noptions = skippable\n"), "o", "o")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Flags.NoBootMarker || !d.Flags.Skippable {
		t.Errorf("flags %+v: no-boot-marker lost to a later options line", d.Flags)
	}
	d, _ = Parse(strings.NewReader("type = internal\noptions = skippable\noptions = starts-rwfs\n"), "o", "o")
	if d.Flags.Skippable || !d.Flags.RWReady {
		t.Errorf("options = still replaces the earlier options: %+v", d.Flags)
	}
}
