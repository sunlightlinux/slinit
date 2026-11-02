package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// versionExempt are commands that must NOT answer --version: they print
// their arguments, as the OpenRC originals they stand in for do, so
// `shell_var --version` has to say "__version".
var versionExempt = map[string]bool{
	"slinit-einfo":     true,
	"slinit-shell-var": true,
}

// versionRE matches the ways a command in cmd/ handles --version: the
// literal, or a flag registered under the name "version".
var versionRE = regexp.MustCompile(`"--version"|Var\(&[A-Za-z_]+, "version"|Bool\("version"`)

// Every Go command answers --version, so an operator or a package can
// ask any of them what it is. 28 did not, and the count had grown from
// the 18 last recorded without anyone noticing; most also lacked the
// `version` variable, so the -X main.version the package build passes
// set nothing.
func TestEveryCommandHasVersion(t *testing.T) {
	root := repoRoot(t)
	dirs, err := filepath.Glob(filepath.Join(root, "cmd", "*"))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, d := range dirs {
		name := filepath.Base(d)
		files, _ := filepath.Glob(filepath.Join(d, "*.go"))
		var src strings.Builder
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			src.Write(b)
		}
		if src.Len() == 0 {
			continue // not a Go command (cmd/slinit-resource is shell)
		}
		checked++
		has := versionRE.MatchString(src.String())
		switch {
		case versionExempt[name] && has:
			t.Errorf("%s handles --version but is exempt: it must print its arguments, as OpenRC's does", name)
		case !versionExempt[name] && !has:
			t.Errorf("%s does not handle --version; add it as the first check in main, alone, so no other argument changes meaning", name)
		}
	}
	if checked < 40 {
		t.Fatalf("checked only %d commands; the glob or the root is wrong", checked)
	}
}
