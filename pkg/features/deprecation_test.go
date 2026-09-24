package features

import "testing"

// TestLookupDeprecation drives the lookup against a table we choose,
// because nothing in the shipped table is deprecated yet — the
// machinery exists so the first deprecation can follow STABILITY.md's
// rule rather than arrive alongside an unimplemented rule.
func TestLookupDeprecation(t *testing.T) {
	table := []Feature{
		{Name: "current-thing", Kind: KindDirective},
		{
			Name:            "old-thing",
			Kind:            KindDirective,
			DeprecatedSince: "2.5.0",
			ReplacedBy:      "use new-thing",
			Aliases:         []string{"older-spelling"},
		},
	}
	index := indexDeprecations(table)

	t.Run("deprecated name", func(t *testing.T) {
		since, replacedBy, ok := lookupDeprecation(index, "old-thing")
		if !ok || since != "2.5.0" || replacedBy != "use new-thing" {
			t.Errorf("got (%q, %q, %v), want (2.5.0, use new-thing, true)", since, replacedBy, ok)
		}
	})

	t.Run("alias of a deprecated name warns too", func(t *testing.T) {
		since, _, ok := lookupDeprecation(index, "older-spelling")
		if !ok || since != "2.5.0" {
			t.Errorf("alias got (%q, %v), want (2.5.0, true)", since, ok)
		}
	})

	t.Run("live directive is not deprecated", func(t *testing.T) {
		if _, _, ok := lookupDeprecation(index, "current-thing"); ok {
			t.Error("a directive with no DeprecatedSince reported as deprecated")
		}
	})

	t.Run("unknown name", func(t *testing.T) {
		if _, _, ok := lookupDeprecation(index, "no-such-thing"); ok {
			t.Error("unknown name reported as deprecated")
		}
	})
}

// TestNothingIsDeprecatedYet pins the claim STABILITY.md makes. When
// the first deprecation lands this test is the reminder that it is a
// minor release, with a CHANGELOG note and a man-page mention — not a
// one-line table edit.
func TestNothingIsDeprecatedYet(t *testing.T) {
	for _, f := range provenanceTable {
		if f.DeprecatedSince == "" {
			continue
		}
		t.Errorf("%s is marked deprecated since %q. That is allowed, but it is "+
			"a minor release under STABILITY.md: check the CHANGELOG has a "+
			"Deprecated note and slinit-service(5) says so, then delete this test.",
			f.Name, f.DeprecatedSince)
	}
}

// TestAliasesAreNotDeprecations guards the distinction STABILITY.md
// draws: the dinit spellings are kept on purpose and permanently, so
// they must never acquire a DeprecatedSince.
func TestAliasesAreNotDeprecations(t *testing.T) {
	for _, name := range []string{"termsignal", "rlimit-addrspace", "run-in-cgroup"} {
		if _, _, ok := Deprecation(name); ok {
			t.Errorf("%s is a dinit-compatibility alias and must stay one; "+
				"STABILITY.md keeps aliases for the life of the major version", name)
		}
	}
}
