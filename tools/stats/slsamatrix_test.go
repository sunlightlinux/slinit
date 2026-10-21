package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The SLSA release matrix, the per-binary configs and cmd/ must agree.
//
// This is hand-maintained in two places that cannot see each other, and
// it has now drifted twice in opposite directions. First some binaries
// were missing from the matrix, so four shipped unsigned — a silent
// failure, because an absent entry builds nothing and complains about
// nothing. Then five matrix entries were added whose config files never
// were, which failed the v3.3.0 release with five build-dry errors and
// no indication in them of what was actually wrong.
//
// A release is the worst place to discover either, so the invariant is
// asserted here instead: every Go main under cmd/ has a matrix entry,
// every matrix entry has a config, and nothing exists on one side
// alone. No count is hardcoded — a hardcoded number is the thing that
// went stale in every config header.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Skipf("not running from a source checkout: %v", err)
	}
	return root
}

// goMains lists cmd/ directories holding at least one non-test .go
// file. cmd/slinit-resource is a shell script with Go tests beside it,
// so counting directories rather than Go mains overcounts by one.
func goMains(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "cmd"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, "cmd", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			n := f.Name()
			if strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
				out = append(out, e.Name())
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func matrixBinaries(t *testing.T, root string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, ".github/workflows/slsa-goreleaser.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// The matrix is a plain YAML list under `binary:`; parsing just that
	// run of list items avoids pulling in a YAML dependency for one file.
	var out []string
	inList := false
	item := regexp.MustCompile(`^\s+- (\S+)\s*$`)
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), "binary:") {
			inList = true
			continue
		}
		if inList {
			m := item.FindStringSubmatch(line)
			if m == nil {
				inList = false
				continue
			}
			out = append(out, m[1])
		}
	}
	if len(out) == 0 {
		t.Fatal("found no binaries in the SLSA matrix — the workflow's shape changed")
	}
	sort.Strings(out)
	return out
}

func configuredBinaries(t *testing.T, root string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, ".slsa-goreleaser/linux-amd64-*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range paths {
		n := strings.TrimSuffix(filepath.Base(p), ".yml")
		out = append(out, strings.TrimPrefix(n, "linux-amd64-"))
	}
	sort.Strings(out)
	return out
}

func diff(a, b []string) []string {
	in := map[string]bool{}
	for _, s := range b {
		in[s] = true
	}
	var only []string
	for _, s := range a {
		if !in[s] {
			only = append(only, s)
		}
	}
	return only
}

func TestSLSAMatrixCoversEveryGoCommand(t *testing.T) {
	root := repoRoot(t)
	mains := goMains(t, root)
	matrix := matrixBinaries(t, root)

	if missing := diff(mains, matrix); len(missing) > 0 {
		t.Errorf("Go commands with no SLSA matrix entry — these would ship UNSIGNED, "+
			"and nothing would say so: %v", missing)
	}
	if extra := diff(matrix, mains); len(extra) > 0 {
		t.Errorf("SLSA matrix entries with no Go main under cmd/: %v", extra)
	}
}

func TestSLSAMatrixEntriesAllHaveConfigs(t *testing.T) {
	root := repoRoot(t)
	matrix := matrixBinaries(t, root)
	configs := configuredBinaries(t, root)

	if missing := diff(matrix, configs); len(missing) > 0 {
		t.Errorf("SLSA matrix entries with no .slsa-goreleaser config — each is a "+
			"build-dry failure at release time: %v", missing)
	}
	if orphan := diff(configs, matrix); len(orphan) > 0 {
		t.Errorf("SLSA configs with no matrix entry (dead files): %v", orphan)
	}
}
