package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// !clear and !unset in the global env-file are documented as honoured,
// but only acted on the variables the file itself set: a service still
// inherited FOO after `!unset FOO`, and everything after `!clear`.
func TestGlobalEnvFileClearsInherited(t *testing.T) {
	for _, c := range []struct {
		name, envFile string
		want, gone    []string
	}{
		{"unset", "!unset FOO\nX=1\n", []string{"X=1", "KEEP=k", "PATH="}, []string{"FOO="}},
		{"clear", "!clear\nX=1\n", []string{"X=1", "PATH="}, []string{"FOO=", "KEEP="}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "envf"), []byte(c.envFile), 0644); err != nil {
				t.Fatal(err)
			}
			writeService(t, dir, "boot", `
type = process
command = /bin/sh -c "env > `+dir+`/out.tmp; mv `+dir+`/out.tmp `+dir+`/out; exec sleep 100"
`)
			startNS(t, dir, `FOO=bar KEEP=k exec "$B/slinit" -u -d "$D/sd" -p "$D/sock" -e "$D/envf" >"$D/log" 2>&1`)
			out := "\n" + waitFile(t, filepath.Join(dir, "out"), 15*time.Second)
			for _, w := range c.want {
				if !strings.Contains(out, "\n"+w) {
					t.Errorf("service environment lacks %s", w)
				}
			}
			for _, g := range c.gone {
				if strings.Contains(out, "\n"+g) {
					t.Errorf("service environment still has %s", g)
				}
			}
			if t.Failed() {
				t.Logf("service environment:%s", out)
			}
		})
	}
}
