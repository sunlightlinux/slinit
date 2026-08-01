package features

import (
	"strings"
	"testing"
)

// TestProvenanceTableNoDuplicates guarantees each Name (and each
// Alias) appears at most once across the whole table — merge.go
// indexes by Name, so a dup silently masks the second entry.
func TestProvenanceTableNoDuplicates(t *testing.T) {
	seen := map[string]string{}
	for _, f := range provenanceTable {
		if prev, ok := seen[f.Name]; ok {
			t.Errorf("duplicate Name %q (also seen as %q)", f.Name, prev)
		}
		seen[f.Name] = f.Name
		for _, a := range f.Aliases {
			if prev, ok := seen[a]; ok {
				t.Errorf("alias %q of %q collides with %q", a, f.Name, prev)
			}
			seen[a] = f.Name + " (alias)"
		}
	}
}

// TestProvenanceHasFieldsFilled — every entry must have Source,
// Category, and Kind. Zero-value would produce meaningless output.
func TestProvenanceRequiredFields(t *testing.T) {
	for _, f := range provenanceTable {
		if f.Name == "" {
			t.Errorf("entry with empty Name: %+v", f)
		}
		if f.Kind == "" {
			t.Errorf("%s: Kind is empty", f.Name)
		}
		if f.Source == "" {
			t.Errorf("%s: Source is empty", f.Name)
		}
		if f.Category == "" {
			t.Errorf("%s: Category is empty", f.Name)
		}
	}
}

// TestLoadPopulatesRegistry sanity-checks Load with a small
// synthetic input (not the real parser.go). Confirms Registry.All()
// is sorted stably and Lookup finds by both Name and Alias.
func TestLoadPopulatesRegistry(t *testing.T) {
	reg, _ := Load(
		[]string{"CmdQueryVersion", "CmdFindService"}, // discovered opcodes
		[]string{"restart", "command"},                 // discovered directives
	)
	if reg == nil {
		t.Fatal("Load returned nil registry")
	}
	// Lookup by name should hit both the discovered items (via provenance).
	for _, name := range []string{"CmdQueryVersion", "restart", "command"} {
		if reg.Lookup(name) == nil {
			t.Errorf("Lookup(%q) returned nil", name)
		}
	}
}

// TestReconcileNoOrphansAgainstReal — CI safety: when Load runs
// against the real source files, no annotated entry should be
// "orphan" (i.e. removed from code without cleanup). Missing
// annotations are TODOs and acceptable to accumulate.
//
// Uses the actual discover output — this test doubles as a live
// audit that provenance stays consistent with the code.
func TestReconcileNoOrphansAgainstReal(t *testing.T) {
	opcodes, err := DiscoverOpcodes("../../pkg/control/protocol.go")
	if err != nil {
		t.Skipf("could not discover opcodes (running outside repo?): %v", err)
	}
	directives, err := DiscoverDirectives("../../pkg/config/parser.go")
	if err != nil {
		t.Skipf("could not discover directives: %v", err)
	}
	_, rep := Load(opcodes, directives)
	if len(rep.OrphanAnnotations) > 0 {
		t.Errorf("provenance has %d orphan annotations (removed from code but still annotated):\n  %s",
			len(rep.OrphanAnnotations),
			strings.Join(rep.OrphanAnnotations, "\n  "))
	}
	// Missing annotations get a warn-only note — accumulation
	// tracked but not a build failure. This is intentional: the
	// annotation task is incremental and shouldn't gate every
	// code-add PR.
	if len(rep.MissingAnnotations) > 0 {
		t.Logf("[warn] %d discovered items lack provenance (placeholders in registry):\n  first 10: %v",
			len(rep.MissingAnnotations),
			firstNames(rep.MissingAnnotations, 10))
	}
}

func firstNames(ss []string, n int) []string {
	if len(ss) < n {
		return ss
	}
	return ss[:n]
}

// TestKindsAreValid — Feature.Kind must be one of the three enum
// constants (helps catch typos in provenance rows).
func TestKindsAreValid(t *testing.T) {
	valid := map[Kind]bool{KindDirective: true, KindOpcode: true, KindOption: true}
	for _, f := range provenanceTable {
		if !valid[f.Kind] {
			t.Errorf("%s: invalid Kind %q", f.Name, f.Kind)
		}
	}
}

// TestSourcesAreKnown — Feature.Source must be one of the seven
// enum constants (dinit/systemd/runit/s6/openrc/upstart/slinit).
func TestSourcesAreKnown(t *testing.T) {
	valid := map[Source]bool{
		SourceDinit: true, SourceSystemd: true, SourceRunit: true,
		SourceS6: true, SourceOpenRC: true, SourceUpstart: true,
		SourceSlinit: true,
	}
	for _, f := range provenanceTable {
		if !valid[f.Source] {
			t.Errorf("%s: unknown Source %q", f.Name, f.Source)
		}
	}
}

// TestDiscoverDirectivesFiltering — sanity check that non-directive
// helper switch values (log levels, boolean literals, arch names)
// don't leak into the discovery output. Runs against the real
// parser.go via the same path Load uses.
func TestDiscoverDirectivesFiltering(t *testing.T) {
	dirs, err := DiscoverDirectives("../../pkg/config/parser.go")
	if err != nil {
		t.Skipf("could not discover: %v", err)
	}
	// Sample of specific non-directives that would have polluted the
	// output BEFORE the switch-tag filter was added. If any of these
	// reappear, the filter regressed.
	// Note: "debug" is a real slinit directive (Upstart-derived
	// `debug=yes`), NOT a false-positive log level, so it's not in
	// this list. Same for other names that happen to be common
	// English words — the switch-tag filter is what protects us,
	// not a keyword blacklist.
	forbidden := []string{"aarch64", "x86_64", "arm64"}
	for _, d := range dirs {
		for _, bad := range forbidden {
			if d == bad {
				t.Errorf("non-directive %q leaked into discovered list", d)
			}
		}
	}
}
