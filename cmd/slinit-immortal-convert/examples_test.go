package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/config"
)

// Every vendored upstream example must convert into something slinit's
// own parser accepts. Synthesized inputs are shapes I imagined; these
// are the ones immortal's author ships, and they caught at once what
// the imagined ones missed — a bare `cmd:` resolved through PATH, which
// slinit does not search, so the output was a service that could not
// start.
func TestVendoredImmortalExamplesConvertAndParse(t *testing.T) {
	dir := filepath.Join("testdata", "immortal-examples")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("no vendored examples: %v", err)
	}

	var checked int
	for _, e := range ents {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yml" {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".yml")
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			cfg, warns, err := parseImmortalYAML(bufio.NewScanner(bytes.NewReader(b)))
			if err != nil {
				// bad-run.yml is deliberately invalid upstream; a
				// refusal is the correct outcome for it.
				if name == "bad-run" {
					return
				}
				t.Fatalf("parse: %v", err)
			}
			_ = warns

			var buf bytes.Buffer
			emitSlinitFile(&buf, cfg, name)
			if _, err := config.Parse(bytes.NewReader(buf.Bytes()), name, "converted"); err != nil {
				t.Fatalf("slinit refused the converted file: %v\n%s", err, buf.String())
			}

			// A converted service must name an absolute command, or
			// slinit will exec something it cannot find.
			for _, line := range strings.Split(buf.String(), "\n") {
				if strings.HasPrefix(line, "command = ") {
					argv0 := strings.Fields(strings.TrimPrefix(line, "command = "))
					if len(argv0) > 0 && !strings.HasPrefix(argv0[0], "/") {
						t.Errorf("command is not absolute: %q", line)
					}
				}
			}
			checked++
		})
	}
	if checked == 0 {
		t.Error("no examples were checked")
	}
}
