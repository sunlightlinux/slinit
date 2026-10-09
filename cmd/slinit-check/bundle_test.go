package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A bundle with no `type` is internal — the loader makes it so — but the
// secondary checks re-parsed the file and saw the parser's default,
// process, so every such bundle drew "no command specified for process
// service".
func TestBundleWithoutTypeIsNotWarnedAboutCommand(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain")
	}
	bin := filepath.Join(t.TempDir(), "slinit-check")
	if out, err := exec.Command(gobin, "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	dir := t.TempDir()
	for name, body := range map[string]string{
		"member": "type = internal\n",
		"group":  "bundle-of = member\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command(bin, "-d", dir, "group").CombinedOutput()
	if err != nil {
		t.Fatalf("slinit-check failed: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "WARNING") {
		t.Errorf("bundle drew a warning:\n%s", out)
	}
}
