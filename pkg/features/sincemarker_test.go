package features

import (
	"bufio"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	baselineFile = "testdata/directives-baseline.txt"
	parserFile   = "../../pkg/config/parser.go"
	manPage      = "../../doc/man/slinit-service.5.md"
)

// sinceMarkerRE matches the marker this test requires, e.g.
//
//	**shiny-new-thing**=*yes*|*no*
//	:   (since 2.5.0) Does the shiny new thing.
//
// The version is not validated beyond its shape: which release a
// directive lands in is not knowable while it is being written.
var sinceMarkerRE = regexp.MustCompile(`\(since \d+\.\d+\.\d+\)`)

func readBaseline(t *testing.T) map[string]bool {
	t.Helper()
	f, err := os.Open(baselineFile)
	if err != nil {
		t.Fatalf("open baseline: %v", err)
	}
	defer f.Close()

	out := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out[line] = true
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	return out
}

// documentedBlock returns the man-page text belonging to a directive:
// its term line plus the indented description under it, up to the next
// term or heading. Empty when the directive is not documented at all.
func documentedBlock(page, name string) string {
	term := regexp.MustCompile(`(?m)^\*\*\\?` + regexp.QuoteMeta(name) + `\*\*[=(\s]`)
	loc := term.FindStringIndex(page)
	if loc == nil {
		return ""
	}
	rest := page[loc[0]:]
	// The block ends at the next line that starts a new term or a
	// heading — i.e. the next line beginning with '*' or '#' after the
	// first one.
	lines := strings.Split(rest, "\n")
	var b strings.Builder
	for i, line := range lines {
		if i > 0 && (strings.HasPrefix(line, "**") || strings.HasPrefix(line, "#")) {
			break
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// TestNewDirectivesCarrySinceMarker is the enforcement half of
// STABILITY.md's commitment that slinit-service(5) marks each new
// directive with the version it appeared in.
//
// It is deliberately narrow. Every directive slinit accepts today
// predates the commitment — 286 shipped by v1.10.55 and no-boot-marker
// at v2.2.9 — so there is nothing to backfill and a blanket
// requirement would fail 287 times on day one for no benefit. The
// baseline file lists what is exempt; anything not in it is new, and
// new is exactly what the commitment is about.
//
// This is not hypothetical bookkeeping. no-boot-marker was added at
// v2.2.9 and was still missing from slinit-service(5) entirely at
// v2.4.0, two years of releases later, because nothing checked.
func TestNewDirectivesCarrySinceMarker(t *testing.T) {
	baseline := readBaseline(t)

	directives, err := DiscoverDirectives(parserFile)
	if err != nil {
		t.Fatalf("discover directives: %v", err)
	}
	pageBytes, err := os.ReadFile(manPage)
	if err != nil {
		t.Fatalf("read man page: %v", err)
	}
	page := string(pageBytes)

	var undocumented, unmarked []string
	for _, name := range directives {
		if baseline[name] {
			continue
		}
		block := documentedBlock(page, name)
		switch {
		case block == "":
			undocumented = append(undocumented, name)
		case !sinceMarkerRE.MatchString(block):
			unmarked = append(unmarked, name)
		}
	}
	sort.Strings(undocumented)
	sort.Strings(unmarked)

	for _, name := range undocumented {
		t.Errorf("%s: new directive, not documented in slinit-service(5) at all. "+
			"Add an entry with a %q marker.", name, "(since X.Y.Z)")
	}
	for _, name := range unmarked {
		t.Errorf("%s: new directive documented without a %q marker. "+
			"STABILITY.md promises operators can tell which release "+
			"introduced a directive; the CHANGELOG is not enough on its own.",
			name, "(since X.Y.Z)")
	}
}

// TestBaselineMatchesParser keeps the exemption list honest in the
// other direction: a name that leaves the parser should leave the
// baseline too, or the file slowly becomes a graveyard that exempts
// directives nobody can use.
func TestBaselineMatchesParser(t *testing.T) {
	baseline := readBaseline(t)

	directives, err := DiscoverDirectives(parserFile)
	if err != nil {
		t.Fatalf("discover directives: %v", err)
	}
	live := map[string]bool{}
	for _, n := range directives {
		live[n] = true
	}

	var stale []string
	for name := range baseline {
		if !live[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	for _, name := range stale {
		t.Errorf("%s is in the baseline but the parser no longer accepts it. "+
			"If it was removed, that is a major-release change under "+
			"STABILITY.md — drop it from %s too.", name, baselineFile)
	}
}

// TestSinceMarkerRecognised guards the regexp itself. A marker format
// that silently matches nothing would make the test above pass for
// every wrong reason.
func TestSinceMarkerRecognised(t *testing.T) {
	for _, tc := range []struct {
		block string
		want  bool
	}{
		{":   (since 2.5.0) Does the thing.", true},
		{":   Does the thing. (since 2.5.0)", true},
		{":   (since 10.20.30) Big numbers are fine.", true},
		{":   Does the thing.", false},
		{":   (since 2.5) Two components is not a version.", false},
		{":   Added in version 2.5.0 — wrong wording.", false},
	} {
		if got := sinceMarkerRE.MatchString(tc.block); got != tc.want {
			t.Errorf("match(%q) = %v, want %v", tc.block, got, tc.want)
		}
	}
}

// TestDocumentedBlockIsolatesOneDirective guards the other half: if
// the block extractor ran past the end of an entry it would find a
// neighbouring directive's marker and pass a directive that has none.
func TestDocumentedBlockIsolatesOneDirective(t *testing.T) {
	page := strings.Join([]string{
		"**alpha**=*x*",
		":   No marker here.",
		"",
		"**beta**=*y*",
		":   (since 2.5.0) Marked.",
		"",
	}, "\n")

	if b := documentedBlock(page, "alpha"); sinceMarkerRE.MatchString(b) {
		t.Errorf("alpha's block leaked into beta's:\n%s", b)
	}
	if b := documentedBlock(page, "beta"); !sinceMarkerRE.MatchString(b) {
		t.Errorf("beta's own marker not found in:\n%s", b)
	}
	if b := documentedBlock(page, "gamma"); b != "" {
		t.Errorf("undocumented directive returned a block: %q", b)
	}
}

// TestBlockKeepsTheWholeEntry: the extractor has to return the term
// line *and* its description, because the marker may sit in either.
func TestBlockKeepsTheWholeEntry(t *testing.T) {
	page := "**shiny-new-thing**=*yes*|*no*\n:   (since 2.5.0) Does the shiny new thing.\n"
	got := documentedBlock(page, "shiny-new-thing")
	for _, want := range []string{"**shiny-new-thing**", "(since 2.5.0)", "shiny new thing."} {
		if !strings.Contains(got, want) {
			t.Errorf("block is missing %q:\n%s", want, got)
		}
	}
}
