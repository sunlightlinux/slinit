package main

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The fuzz job in tests.yml lists packages by hand; it must name exactly
// the packages that hold a Fuzz target.
//
// The job itself catches an entry with no targets left (it fails with
// "the matrix entry is stale"), but only at CI time, and it cannot see a
// package that was never listed: three packages had targets CI never
// fuzzed for a month, and moving the hostnamectl/timedatectl targets
// into pkg/ left two stale entries behind. Both directions are checked
// here, before a push.
func TestFuzzMatrixMatchesFuzzTargets(t *testing.T) {
	root := repoRoot(t)

	have := map[string]bool{}
	fuzzFunc := regexp.MustCompile(`^func Fuzz[A-Za-z0-9_]*\(`)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == ".git" || n == "testdata" || n == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if fuzzFunc.MatchString(sc.Text()) {
				rel, _ := filepath.Rel(root, filepath.Dir(p))
				have[filepath.ToSlash(rel)] = true
				break
			}
		}
		return sc.Err()
	})
	if err != nil {
		t.Fatal(err)
	}

	wf, err := os.ReadFile(filepath.Join(root, ".github/workflows/tests.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// The package list of the fuzz job: from "  fuzz:" to its "steps:".
	s := string(wf)
	i := strings.Index(s, "\n  fuzz:\n")
	if i < 0 {
		t.Fatal("no fuzz job in tests.yml")
	}
	j := strings.Index(s[i:], "\n    steps:")
	if j < 0 {
		t.Fatal("fuzz job has no steps")
	}
	listed := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s+- ([a-z][\w./-]*)\s*$`).FindAllStringSubmatch(s[i:i+j], -1) {
		listed[m[1]] = true
	}
	if len(listed) == 0 {
		t.Fatal("could not read the fuzz job's package list")
	}

	var missing, stale []string
	for p := range have {
		if !listed[p] {
			missing = append(missing, p)
		}
	}
	for p := range listed {
		if !have[p] {
			stale = append(stale, p)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("packages with Fuzz targets that CI never fuzzes (add them to the fuzz matrix in tests.yml): %v", missing)
	}
	if len(stale) > 0 {
		t.Errorf("fuzz matrix entries with no Fuzz target (remove them from tests.yml): %v", stale)
	}
}
