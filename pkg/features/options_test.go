package features_test

import (
	"testing"

	"github.com/sunlightlinux/slinit/pkg/config"
	"github.com/sunlightlinux/slinit/pkg/features"
)

// Options are not discovered from the source, unlike directives and
// opcodes: the provenance table is their only record, and it feeds
// doc/features.md, slinit-supports and the project statistics.
// stops-last (3.22.0) was added to the parser and missed here, so every
// one of those said slinit had 13 options when it had 14. Each option
// the parser accepts must be in the table.
func TestEveryParserOptionIsInTheFeatureTable(t *testing.T) {
	reg, _ := features.Load(nil, nil)
	for name := range config.OptionFlags {
		f := reg.Lookup("option:" + name)
		if f == nil || f.Kind != features.KindOption {
			t.Errorf("option %q is accepted by the parser but missing from pkg/features/provenance.go", name)
		}
	}
}
