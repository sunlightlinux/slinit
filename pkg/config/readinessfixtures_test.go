package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Service fixtures that use ready-notification must write a NEWLINE, and
// the obvious way to write one does not work.
//
// Since 3.3.0 readiness is the newline, matching s6. The trap is that
// `command =` is parsed by slinit before the shell ever sees it, and the
// parser eats a single backslash: `printf 'r\n'` reaches the shell as
// `printf 'rn'` and signals nothing. Measured, not assumed — the parser
// turns "printf 'r\n'" into "printf 'rn'" and "printf 'r\\n'" into
// "printf 'r\n'".
//
// That is invisible on inspection, which is how the 3.3.0 change shipped
// with tests/functional/cases/67-watchdog.d/watchdog-svc still sending a
// bare byte: a grep for readiness writes used --include=*.sh and *.go,
// and service definitions in .d/ directories have NO extension, so the
// file was never searched. The functional suite caught it, one release
// later.
//
// `echo` is the form to use: it supplies the newline itself, needs no
// escaping, and was already the idiom in the fixtures that were correct.
func TestReadinessFixturesWriteANewline(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Skipf("not a source checkout: %v", err)
	}

	// Any file, whatever its name — that filter is what caused the miss.
	var checked int
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && info.IsDir() && (info.Name() == ".git" || info.Name() == "_output") {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Size() > 64*1024 {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		text := string(b)
		if !strings.Contains(text, "ready-notification") {
			return nil
		}
		// Only real service definitions: a .md or a Go test is prose.
		rel, _ := filepath.Rel(root, path)
		if strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, ".txt") || strings.Contains(rel, "CHANGELOG") {
			return nil
		}

		for _, line := range strings.Split(text, "\n") {
			if !strings.Contains(line, ">&3") && !strings.Contains(line, "NOTIFY_FD") {
				continue
			}
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			// The directive line itself names the variable; it is not a
			// write to it.
			if strings.HasPrefix(trimmed, "ready-notification") {
				continue
			}
			checked++
			// The parser trap: a single-backslash \n inside a command.
			if regexp.MustCompile(`[^\\]\\n`).MatchString(line) &&
				!strings.Contains(line, `\\n`) {
				t.Errorf("%s: `\\n` with one backslash is eaten by the config parser, "+
					"so this reaches the shell as a literal 'n' and never signals ready:\n  %s",
					rel, strings.TrimSpace(line))
			}
			// And the write must produce a newline somehow.
			writesNewline := strings.Contains(line, "echo") ||
				strings.Contains(line, `\\n`) ||
				strings.Contains(line, `\n`)
			if !writesNewline {
				t.Errorf("%s: readiness write produces no newline, so the service "+
					"would never reach STARTED:\n  %s", rel, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Error("found no readiness writes to check — the search stopped working")
	}
	t.Logf("checked %d readiness write(s)", checked)
}
