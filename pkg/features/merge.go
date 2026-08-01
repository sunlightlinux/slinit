package features

import (
	"fmt"
	"sort"
	"strings"
)

// Load builds a Registry from the two sides:
//   - auto-discovered names (canonical, from source code)
//   - hand-curated provenance annotations (this package's
//     provenanceTable)
//
// Discovered items not present in provenanceTable get placeholder
// entries with Source=SourceSlinit and Notes="TODO: annotate
// provenance". This is intentional — the annotation task is
// incremental and shouldn't gate every code-add PR on a full
// provenance-lookup exercise. Reconcile reports (below) let CI
// track the TODO backlog explicitly.
//
// Provenance annotations for items NOT in the discovered lists
// (dead annotations for removed code) surface via the
// OrphanAnnotations report so a subsequent commit can drop them.
func Load(discoveredOpcodes, discoveredDirectives []string) (*Registry, ReconcileReport) {
	// Fast lookups.
	discovered := map[string]Kind{}
	for _, n := range discoveredOpcodes {
		discovered[n] = KindOpcode
	}
	for _, n := range discoveredDirectives {
		discovered[n] = KindDirective
	}

	// Index provenance by (Name, Kind) for the merge step.
	// Options — items where Kind=KindOption — always come from the
	// manual table (options aren't switch-case values, they're
	// values inside the `options` list; enumerating them from AST
	// would need a second parser pass and there aren't many, so
	// they're all handcurated).
	provByName := map[string]*Feature{}
	for i := range provenanceTable {
		f := &provenanceTable[i]
		provByName[f.Name] = f
		for _, a := range f.Aliases {
			provByName[a] = f
		}
	}

	registry := &Registry{
		byName: map[string]*Feature{},
	}
	report := ReconcileReport{}

	// Walk provenance first — every annotated entry gets registered.
	// Discovered coverage is checked in the second pass.
	for i := range provenanceTable {
		f := &provenanceTable[i]
		registry.byName[f.Name] = f
		for _, a := range f.Aliases {
			registry.byName[a] = f
		}
		// Options are exempt from discovery — they're not switch
		// case values. Opcodes and Directives need to appear in
		// the discovered lists, except @-prefixed structural
		// directives (@include, @include-opt, @meta:*) which the
		// parser handles as a preprocessing step before applySetting
		// sees the line.
		switch f.Kind {
		case KindOption:
			// no discovery check
		case KindOpcode, KindDirective:
			if strings.HasPrefix(f.Name, "@") {
				continue // structural directive; not a switch case
			}
			if _, ok := discovered[f.Name]; !ok {
				report.OrphanAnnotations = append(report.OrphanAnnotations, f.Name)
			}
		}
	}

	// Second pass: discovered items lacking provenance get
	// synthesized placeholders + TODO note. New Feature values
	// end up owned by the registry (no aliasing to the
	// provenanceTable slice since these aren't in it).
	for name, kind := range discovered {
		if _, ok := provByName[name]; ok {
			continue
		}
		placeholder := &Feature{
			Name:     name,
			Kind:     kind,
			Source:   SourceSlinit,
			Category: CatServiceConfig, // most-likely default
			Notes:    "TODO: annotate provenance (auto-placeholder from discover.go)",
		}
		if kind == KindOpcode {
			placeholder.Category = CatControlProto
		}
		registry.byName[name] = placeholder
		report.MissingAnnotations = append(report.MissingAnnotations, name)
	}

	// Populate All in stable order: Opcode < Option < Directive
	// (grouping ops, then user-facing directives), name within each.
	all := make([]*Feature, 0, len(registry.byName))
	seen := map[*Feature]bool{}
	for _, f := range registry.byName {
		if seen[f] {
			continue // alias already added
		}
		seen[f] = true
		all = append(all, f)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Kind != all[j].Kind {
			return kindOrder(all[i].Kind) < kindOrder(all[j].Kind)
		}
		return all[i].Name < all[j].Name
	})
	registry.all = all
	sort.Strings(report.OrphanAnnotations)
	sort.Strings(report.MissingAnnotations)
	return registry, report
}

// kindOrder gives a stable sort ordering across Kind values.
func kindOrder(k Kind) int {
	switch k {
	case KindOpcode:
		return 0
	case KindOption:
		return 1
	case KindDirective:
		return 2
	default:
		return 99
	}
}

// ReconcileReport surfaces the gap between the auto-discovered
// canonical list and the hand-curated provenance table.
//
//   - OrphanAnnotations: entries in provenanceTable that no
//     longer appear in the source code. Almost always means a
//     directive was removed but its annotation stayed — the fix
//     is to delete the row.
//   - MissingAnnotations: names the auto-discovery found that
//     nobody annotated yet. Registry gives them a TODO placeholder;
//     enrichment is incremental work.
//
// CI test's job is to expose these lists; whether the CI FAILS on
// non-empty is a policy call (currently: warn-only in the standard
// test, fail-on-orphan in a strict mode).
type ReconcileReport struct {
	OrphanAnnotations  []string
	MissingAnnotations []string
}

// String renders the report in a compact human-readable form so
// operators + CI logs can consume it directly.
func (r ReconcileReport) String() string {
	return fmt.Sprintf("features: %d orphan annotations, %d missing annotations",
		len(r.OrphanAnnotations), len(r.MissingAnnotations))
}
