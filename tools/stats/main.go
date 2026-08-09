// tools/stats — dev-only project statistics for slinit.
//
// Walks the repo root (defaults to CWD, override with --root) and
// reports:
//
//	* Lines of code by language, split into code / comments / blank.
//	  Recognised: Go, Shell (.sh / .bash), Markdown, YAML, Makefile,
//	  and a slinit-specific "Service config" bucket for the
//	  dinit-style key=value files that don't carry a file extension.
//	* Test counts: `func Test*` and `func Fuzz*` across every
//	  `*_test.go`, plus per-suite functional and acceptance case
//	  file counts.
//	* Structure counts: Go packages under pkg/, binary dirs under
//	  cmd/, demo services, man pages.
//	* Documentation size: CHANGELOG version count + LOC, doc/ LOC.
//	* Feature surface: directive count (from pkg/config/parser.go
//	  case labels) + opcode count (from pkg/control/protocol.go
//	  Cmd*/Rply* constants). Grepped directly out of source so the
//	  tool doesn't need any built binaries to run.
//
// Not part of the shipped slpkgs template — deliberately excluded
// so operator systems don't carry the dev tooling. Build locally
// via `go build -o /tmp/slinit-stats ./tools/stats && /tmp/slinit-stats`.
//
// Output modes: text (default, table-style), --json (machine),
// --markdown (README/CHANGELOG embed).
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------
// Data shapes
// ---------------------------------------------------------------------

// LangStats counts lines for one language bucket. Total is the file
// byte-line count; the code/comments/blank triple always sums to
// Total. Files is the number of source files contributing.
type LangStats struct {
	Files    int `json:"files"`
	Total    int `json:"total_lines"`
	Code     int `json:"code_lines"`
	Comments int `json:"comment_lines"`
	Blank    int `json:"blank_lines"`
}

// TestStats aggregates the four test suites slinit runs. For the
// two shell-driven suites we track both the raw file count and the
// "real cases" count (files that actually assert something, i.e.
// excluding the `999-cleanup.sh` teardown scripts). They usually
// differ by 1 per suite — surfaces the number an operator cares
// about ("tests I run for value") alongside the on-disk truth.
type TestStats struct {
	UnitFuncs        int      `json:"unit_test_funcs"`
	FuzzFuncs        int      `json:"fuzz_targets"`
	FuzzFiles        []string `json:"fuzz_files,omitempty"`
	FunctionalFiles  int      `json:"functional_files"`
	FunctionalCases  int      `json:"functional_cases"`
	AcceptanceFiles  int      `json:"acceptance_files"`
	AcceptanceCases  int      `json:"acceptance_cases"`
}

// StructStats counts top-level project shape.
type StructStats struct {
	Packages     int `json:"packages"`
	Binaries     int `json:"binaries"`
	DemoServices int `json:"demo_services"`
	ManPages     int `json:"man_pages"`
}

// DocStats measures the operator-facing narrative surface.
type DocStats struct {
	ChangelogVersions int `json:"changelog_versions"`
	ChangelogLines    int `json:"changelog_lines"`
	DocDirLines       int `json:"doc_dir_lines"`
	ReadmeLines       int `json:"readme_lines"`
}

// FeatureStats counts the config + wire surface without needing the
// built binaries — greps `case "X":` labels straight out of source
// so the tool runs against a fresh checkout.
type FeatureStats struct {
	Directives int `json:"directives"`
	Opcodes    int `json:"wire_opcodes"`
}

// Stats is the full report.
type Stats struct {
	Root      string                `json:"root"`
	Languages map[string]*LangStats `json:"languages"`
	Grand     LangStats             `json:"grand_total"`
	Tests     TestStats             `json:"tests"`
	Structure StructStats           `json:"structure"`
	Docs      DocStats              `json:"docs"`
	Features  FeatureStats          `json:"features"`
}

// ---------------------------------------------------------------------
// Language detection
// ---------------------------------------------------------------------

// langOf classifies a path into a language bucket by extension +
// filename heuristic. Returns "" to skip the file.
func langOf(path string) string {
	base := filepath.Base(path)
	switch {
	case strings.HasSuffix(base, ".go"):
		return "Go"
	case strings.HasSuffix(base, ".sh"), strings.HasSuffix(base, ".bash"):
		return "Shell"
	case strings.HasSuffix(base, ".md"):
		return "Markdown"
	case strings.HasSuffix(base, ".yaml"), strings.HasSuffix(base, ".yml"):
		return "YAML"
	case strings.HasSuffix(base, ".xml"):
		return "XML"
	case strings.HasSuffix(base, ".json"):
		return "JSON"
	case base == "Makefile", base == "GNUmakefile", strings.HasSuffix(base, ".mk"):
		return "Makefile"
	}
	return ""
}

// ---------------------------------------------------------------------
// Line counting
// ---------------------------------------------------------------------

// commentRulesByLang describes how to classify a line as comment
// vs code vs blank, per language. Simple line-start check — good
// enough for stats; a full lexer would over-invest for the
// deliverable.
type commentRule struct {
	linePrefix []string // any of these at the trimmed line start means "comment"
	// Block comments (/* ... */) tracked separately when non-nil.
	blockStart string
	blockEnd   string
}

var commentRules = map[string]commentRule{
	"Go":       {linePrefix: []string{"//"}, blockStart: "/*", blockEnd: "*/"},
	"Shell":    {linePrefix: []string{"#"}},
	"Markdown": {linePrefix: []string{"<!--"}, blockStart: "<!--", blockEnd: "-->"},
	"YAML":     {linePrefix: []string{"#"}},
	"XML":      {blockStart: "<!--", blockEnd: "-->"},
	"JSON":     {}, // JSON has no comments per spec
	"Makefile": {linePrefix: []string{"#"}},
}

// countLines reads path, classifies each line under lang's rules,
// and returns (total, code, comments, blank). Unknown lang falls
// back to all-code.
func countLines(path, lang string) (LangStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return LangStats{}, err
	}
	defer f.Close()

	rule := commentRules[lang]
	inBlock := false

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	var out LangStats
	out.Files = 1
	for scanner.Scan() {
		out.Total++
		trimmed := strings.TrimSpace(scanner.Text())
		if trimmed == "" {
			out.Blank++
			continue
		}
		if inBlock {
			out.Comments++
			if rule.blockEnd != "" && strings.Contains(trimmed, rule.blockEnd) {
				inBlock = false
			}
			continue
		}
		if rule.blockStart != "" && strings.HasPrefix(trimmed, rule.blockStart) {
			out.Comments++
			if !strings.Contains(trimmed[len(rule.blockStart):], rule.blockEnd) {
				inBlock = true
			}
			continue
		}
		commented := false
		for _, p := range rule.linePrefix {
			if strings.HasPrefix(trimmed, p) {
				commented = true
				break
			}
		}
		if commented {
			out.Comments++
		} else {
			out.Code++
		}
	}
	return out, scanner.Err()
}

// ---------------------------------------------------------------------
// Walker + skip rules
// ---------------------------------------------------------------------

// skipDirNames names directory basenames that we walk into but count
// specially or exclude from source LOC (test fixtures, generated
// output, third-party mirrors).
var skipDirNames = map[string]bool{
	".git":              true,
	"_build":            true,
	"_output":           true,
	"node_modules":      true,
	"vendor":            true, // Go vendor tree (rare in slinit)
	"testdata":          true, // Go convention — fixtures, not source
}

// walkSource walks root and returns per-language stats plus grand
// totals. Files with a langOf() == "" are skipped silently.
func walkSource(root string) (map[string]*LangStats, LangStats, error) {
	langs := map[string]*LangStats{}
	var grand LangStats

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Read errors on a subtree shouldn't abort the whole
			// walk — the operator cares about "how much did we
			// count", not "did every file open cleanly".
			return nil
		}
		if d.IsDir() {
			if skipDirNames[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		lang := langOf(path)
		if lang == "" {
			return nil
		}
		stats, err := countLines(path, lang)
		if err != nil {
			return nil
		}
		agg, ok := langs[lang]
		if !ok {
			agg = &LangStats{}
			langs[lang] = agg
		}
		agg.Files += stats.Files
		agg.Total += stats.Total
		agg.Code += stats.Code
		agg.Comments += stats.Comments
		agg.Blank += stats.Blank

		grand.Files += stats.Files
		grand.Total += stats.Total
		grand.Code += stats.Code
		grand.Comments += stats.Comments
		grand.Blank += stats.Blank
		return nil
	})
	return langs, grand, err
}

// ---------------------------------------------------------------------
// Test counting
// ---------------------------------------------------------------------

var (
	testFuncRE = regexp.MustCompile(`(?m)^func Test[A-Z_]`)
	fuzzFuncRE = regexp.MustCompile(`(?m)^func Fuzz[A-Z_]`)
)

// countTests grinds through the tree once, counting `func Test*` and
// `func Fuzz*` in every `_test.go`, then counts the case files under
// the two shell-driven suites.
func countTests(root string) (TestStats, error) {
	var out TestStats

	// Unit + fuzz.
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirNames[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if fuzzHits := fuzzFuncRE.FindAllIndex(data, -1); len(fuzzHits) > 0 {
			out.FuzzFuncs += len(fuzzHits)
			// Record the file so an operator can jump to it.
			rel, _ := filepath.Rel(root, path)
			out.FuzzFiles = append(out.FuzzFiles, rel)
		}
		out.UnitFuncs += len(testFuncRE.FindAllIndex(data, -1))
		return nil
	})
	if err != nil {
		return out, err
	}
	sort.Strings(out.FuzzFiles)

	// Functional + acceptance: files-on-disk (raw count) AND real
	// cases (excludes 999-cleanup.sh). Both surfaced so the
	// operator sees the ground-truth AND the value-yielding
	// subset.
	out.FunctionalFiles, out.FunctionalCases = countCaseFiles(filepath.Join(root, "tests/functional/cases"))
	out.AcceptanceFiles, out.AcceptanceCases = countCaseFiles(filepath.Join(root, "tests/acceptance/ssh/cases"))
	return out, nil
}

// countCaseFiles returns (files_on_disk, real_cases) for NN-*.sh
// scripts in a case dir. real_cases excludes `999-cleanup.sh` (a
// teardown script that resets state but doesn't test anything).
// Returns (0, 0) on missing dir.
func countCaseFiles(dir string) (int, int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	var files, cases int
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(name, ".sh") {
			continue
		}
		files++
		if name != "999-cleanup.sh" {
			cases++
		}
	}
	return files, cases
}

// ---------------------------------------------------------------------
// Structural counts
// ---------------------------------------------------------------------

func structStats(root string) StructStats {
	return StructStats{
		Packages:     countGoPkgs(filepath.Join(root, "pkg")),
		Binaries:     countChildDirs(filepath.Join(root, "cmd")),
		DemoServices: countRegularFiles(filepath.Join(root, "demo", "services")),
		ManPages:     countManPages(filepath.Join(root, "doc", "man")),
	}
}

// countGoPkgs counts direct subdirs of root that contain at least
// one *.go source file.
func countGoPkgs(root string) int {
	var n int
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		if hasGoFile(filepath.Join(root, ent.Name())) {
			n++
		}
	}
	return n
}

// hasGoFile recursively searches root for any .go file. Stops at
// first match.
func hasGoFile(root string) bool {
	found := false
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func countChildDirs(root string) int {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	var n int
	for _, ent := range entries {
		if ent.IsDir() {
			n++
		}
	}
	return n
}

func countRegularFiles(root string) int {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	var n int
	for _, ent := range entries {
		if !ent.IsDir() {
			n++
		}
	}
	return n
}

// countManPages counts man source files under dir. Recognises both
// rendered groff (`.1`..`.9`) and go-md2man source (`.N.md`) since
// slinit ships the latter.
var manPageRE = regexp.MustCompile(`\.[1-9](\.md)?$`)

func countManPages(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var n int
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		if manPageRE.MatchString(ent.Name()) {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------
// Docs + CHANGELOG
// ---------------------------------------------------------------------

func docStats(root string) DocStats {
	var out DocStats
	if data, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md")); err == nil {
		out.ChangelogLines = strings.Count(string(data), "\n")
		re := regexp.MustCompile(`(?m)^## \[[0-9]+\.[0-9]+\.[0-9]+`)
		out.ChangelogVersions = len(re.FindAllIndex(data, -1))
	}
	if data, err := os.ReadFile(filepath.Join(root, "README.md")); err == nil {
		out.ReadmeLines = strings.Count(string(data), "\n")
	}
	out.DocDirLines = sumFileLines(filepath.Join(root, "doc"))
	return out
}

// sumFileLines totals `wc -l`-equivalent line counts across every
// regular file under root. Newline-count based so a file without a
// trailing newline is not misjudged.
func sumFileLines(root string) int {
	var n int
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		n += strings.Count(string(data), "\n")
		return nil
	})
	return n
}

// ---------------------------------------------------------------------
// Feature surface — greps source rather than shelling out to the
// installed slinit-supports binary, so the tool works on a fresh
// checkout with nothing built.
// ---------------------------------------------------------------------

var (
	directiveCaseRE = regexp.MustCompile(`(?m)^\s*case\s+"([a-z][a-z0-9-]+)"\s*:`)
	opcodeConstRE   = regexp.MustCompile(`(?m)^\s*(Cmd|Rply)[A-Z][A-Za-z0-9]*\s*(=\s*|CommandCode\s*=\s*|\s+CommandCode\s*=\s*|\s+ReplyCode\s*=\s*)`)
)

func featureStats(root string) FeatureStats {
	var out FeatureStats
	if data, err := os.ReadFile(filepath.Join(root, "pkg/config/parser.go")); err == nil {
		// Dedupe: some case labels appear inside a nested switch
		// (options bag). Set count is the honest number of
		// distinct directive names.
		seen := map[string]bool{}
		for _, m := range directiveCaseRE.FindAllSubmatch(data, -1) {
			seen[string(m[1])] = true
		}
		out.Directives = len(seen)
	}
	// Opcodes: grep pkg/control/protocol.go for `Cmd*` and `Rply*`
	// constant names. Simpler than pulling in the binary.
	if data, err := os.ReadFile(filepath.Join(root, "pkg/control/protocol.go")); err == nil {
		re := regexp.MustCompile(`(?m)^\s*(Cmd[A-Z][A-Za-z0-9]*|Rply[A-Z][A-Za-z0-9]*)\b`)
		seen := map[string]bool{}
		for _, m := range re.FindAllSubmatch(data, -1) {
			seen[string(m[1])] = true
		}
		out.Opcodes = len(seen)
	}
	return out
}

// ---------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------

func renderText(s *Stats, w *os.File) {
	fmt.Fprintf(w, "slinit project statistics — root: %s\n\n", s.Root)

	// Languages table.
	fmt.Fprintf(w, "== LOC by language ==\n")
	fmt.Fprintf(w, "%-14s %8s %8s %8s %8s %8s\n", "LANGUAGE", "FILES", "TOTAL", "CODE", "COMMENT", "BLANK")
	fmt.Fprintf(w, "%s\n", strings.Repeat("-", 60))
	langs := sortedLangs(s.Languages)
	for _, l := range langs {
		v := s.Languages[l]
		fmt.Fprintf(w, "%-14s %8d %8d %8d %8d %8d\n", l, v.Files, v.Total, v.Code, v.Comments, v.Blank)
	}
	fmt.Fprintf(w, "%s\n", strings.Repeat("-", 60))
	fmt.Fprintf(w, "%-14s %8d %8d %8d %8d %8d\n", "TOTAL",
		s.Grand.Files, s.Grand.Total, s.Grand.Code, s.Grand.Comments, s.Grand.Blank)

	fmt.Fprintln(w)
	fmt.Fprintf(w, "== Tests ==\n")
	fmt.Fprintf(w, "  Unit  (func Test*):   %d\n", s.Tests.UnitFuncs)
	fmt.Fprintf(w, "  Fuzz  (func Fuzz*):   %d across %d file(s)\n", s.Tests.FuzzFuncs, len(s.Tests.FuzzFiles))
	fmt.Fprintf(w, "  Functional (QEMU):    %d cases (%d files on disk)\n",
		s.Tests.FunctionalCases, s.Tests.FunctionalFiles)
	fmt.Fprintf(w, "  Acceptance (SSH):     %d cases (%d files on disk)\n",
		s.Tests.AcceptanceCases, s.Tests.AcceptanceFiles)
	fmt.Fprintf(w, "  Grand total (cases):  %d\n",
		s.Tests.UnitFuncs+s.Tests.FuzzFuncs+s.Tests.FunctionalCases+s.Tests.AcceptanceCases)

	fmt.Fprintln(w)
	fmt.Fprintf(w, "== Structure ==\n")
	fmt.Fprintf(w, "  Go packages under pkg/:   %d\n", s.Structure.Packages)
	fmt.Fprintf(w, "  Binary dirs under cmd/:   %d\n", s.Structure.Binaries)
	fmt.Fprintf(w, "  Demo service files:       %d\n", s.Structure.DemoServices)
	fmt.Fprintf(w, "  Man pages:                %d\n", s.Structure.ManPages)

	fmt.Fprintln(w)
	fmt.Fprintf(w, "== Feature surface ==\n")
	fmt.Fprintf(w, "  Config directives:        %d\n", s.Features.Directives)
	fmt.Fprintf(w, "  Wire opcodes (Cmd+Rply):  %d\n", s.Features.Opcodes)

	fmt.Fprintln(w)
	fmt.Fprintf(w, "== Docs ==\n")
	fmt.Fprintf(w, "  CHANGELOG versions:       %d\n", s.Docs.ChangelogVersions)
	fmt.Fprintf(w, "  CHANGELOG lines:          %d\n", s.Docs.ChangelogLines)
	fmt.Fprintf(w, "  doc/ dir lines:           %d\n", s.Docs.DocDirLines)
	fmt.Fprintf(w, "  README lines:             %d\n", s.Docs.ReadmeLines)
}

func renderJSON(s *Stats, w *os.File) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}

func renderMarkdown(s *Stats, w *os.File) {
	fmt.Fprintf(w, "# slinit project stats\n\n")
	fmt.Fprintf(w, "Root: `%s`\n\n", s.Root)

	fmt.Fprintf(w, "## LOC by language\n\n")
	fmt.Fprintf(w, "| Language | Files | Total | Code | Comment | Blank |\n")
	fmt.Fprintf(w, "|---|---:|---:|---:|---:|---:|\n")
	for _, l := range sortedLangs(s.Languages) {
		v := s.Languages[l]
		fmt.Fprintf(w, "| %s | %d | %d | %d | %d | %d |\n", l, v.Files, v.Total, v.Code, v.Comments, v.Blank)
	}
	fmt.Fprintf(w, "| **TOTAL** | **%d** | **%d** | **%d** | **%d** | **%d** |\n\n",
		s.Grand.Files, s.Grand.Total, s.Grand.Code, s.Grand.Comments, s.Grand.Blank)

	fmt.Fprintf(w, "## Tests\n\n")
	fmt.Fprintf(w, "| Suite | Cases | Files on disk |\n|---|---:|---:|\n")
	fmt.Fprintf(w, "| Unit (`func Test*`) | %d | — |\n", s.Tests.UnitFuncs)
	fmt.Fprintf(w, "| Fuzz (`func Fuzz*`) | %d | %d |\n", s.Tests.FuzzFuncs, len(s.Tests.FuzzFiles))
	fmt.Fprintf(w, "| Functional (QEMU) | %d | %d |\n", s.Tests.FunctionalCases, s.Tests.FunctionalFiles)
	fmt.Fprintf(w, "| Acceptance (SSH) | %d | %d |\n", s.Tests.AcceptanceCases, s.Tests.AcceptanceFiles)
	fmt.Fprintf(w, "| **Grand total (cases)** | **%d** | |\n\n",
		s.Tests.UnitFuncs+s.Tests.FuzzFuncs+s.Tests.FunctionalCases+s.Tests.AcceptanceCases)

	fmt.Fprintf(w, "## Structure\n\n")
	fmt.Fprintf(w, "| Metric | Count |\n|---|---:|\n")
	fmt.Fprintf(w, "| Go packages under `pkg/` | %d |\n", s.Structure.Packages)
	fmt.Fprintf(w, "| Binary dirs under `cmd/` | %d |\n", s.Structure.Binaries)
	fmt.Fprintf(w, "| Demo service files | %d |\n", s.Structure.DemoServices)
	fmt.Fprintf(w, "| Man pages | %d |\n\n", s.Structure.ManPages)

	fmt.Fprintf(w, "## Feature surface\n\n")
	fmt.Fprintf(w, "| Surface | Count |\n|---|---:|\n")
	fmt.Fprintf(w, "| Config directives | %d |\n", s.Features.Directives)
	fmt.Fprintf(w, "| Wire opcodes (Cmd+Rply) | %d |\n\n", s.Features.Opcodes)

	fmt.Fprintf(w, "## Docs\n\n")
	fmt.Fprintf(w, "| Doc | Value |\n|---|---:|\n")
	fmt.Fprintf(w, "| CHANGELOG versions | %d |\n", s.Docs.ChangelogVersions)
	fmt.Fprintf(w, "| CHANGELOG lines | %d |\n", s.Docs.ChangelogLines)
	fmt.Fprintf(w, "| `doc/` lines | %d |\n", s.Docs.DocDirLines)
	fmt.Fprintf(w, "| README lines | %d |\n", s.Docs.ReadmeLines)
}

// sortedLangs returns language keys ordered by CODE lines desc,
// so the biggest language shows first in every renderer.
func sortedLangs(m map[string]*LangStats) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return m[keys[i]].Code > m[keys[j]].Code
	})
	return keys
}

// ---------------------------------------------------------------------
// main
// ---------------------------------------------------------------------

func main() {
	var (
		root     = flag.String("root", ".", "slinit repo root")
		asJSON   = flag.Bool("json", false, "emit JSON instead of the text table")
		asMD     = flag.Bool("markdown", false, "emit Markdown tables (embed in README/CHANGELOG)")
	)
	flag.Parse()

	abs, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stats: resolve root: %v\n", err)
		os.Exit(1)
	}

	// Sanity check — bail early if the root doesn't look like a
	// slinit checkout so we don't produce nonsense stats against a
	// random directory.
	if _, err := os.Stat(filepath.Join(abs, "cmd", "slinit")); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "stats: %s does not look like the slinit repo (no cmd/slinit dir)\n", abs)
		os.Exit(1)
	}

	langs, grand, err := walkSource(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stats: source walk: %v\n", err)
		os.Exit(1)
	}
	tests, err := countTests(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stats: test walk: %v\n", err)
		os.Exit(1)
	}

	s := &Stats{
		Root:      abs,
		Languages: langs,
		Grand:     grand,
		Tests:     tests,
		Structure: structStats(abs),
		Docs:      docStats(abs),
		Features:  featureStats(abs),
	}

	switch {
	case *asJSON:
		if err := renderJSON(s, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "stats: json: %v\n", err)
			os.Exit(1)
		}
	case *asMD:
		renderMarkdown(s, os.Stdout)
	default:
		renderText(s, os.Stdout)
	}
}
