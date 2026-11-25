package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestMain runs main itself when re-executed by runConverter, so the
// stderr checks exercise the real flag handling and printing.
func TestMain(m *testing.M) {
	if os.Getenv("SLINIT_CONVERT_RUN_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runConverter runs the converter's main with args and returns stderr.
func runConverter(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "SLINIT_CONVERT_RUN_MAIN=1")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("converter %v: %v\n%s", args, err, stderr.String())
	}
	return stderr.String()
}

// checkWarnsByDefault: a warning is printed without --verbose, a note
// only with it. Both used to be --verbose only, so a plain conversion
// that lost something said nothing at all.
func checkWarnsByDefault(t *testing.T, args ...string) {
	t.Helper()
	quiet := runConverter(t, args...)
	if !strings.Contains(quiet, "[WARN]") {
		t.Errorf("no [WARN] without --verbose:\n%s", quiet)
	}
	if strings.Contains(quiet, "[NOTE]") {
		t.Errorf("[NOTE] printed without --verbose:\n%s", quiet)
	}
	loud := runConverter(t, append([]string{"--verbose"}, args...)...)
	if !strings.Contains(loud, "[WARN]") || !strings.Contains(loud, "[NOTE]") {
		t.Errorf("--verbose must print both levels:\n%s", loud)
	}
}

func TestWarningsPrintByDefault(t *testing.T) {
	dir := t.TempDir()
	// chpst -U is a WARN (not mapped); a run script with no exec line
	// is wrapped with a NOTE.
	for name, run := range map[string]string{"w": "exec chpst -U bob /bin/true", "n": "/bin/true"} {
		os.MkdirAll(dir+"/"+name, 0o755)
		os.WriteFile(dir+"/"+name+"/run", []byte("#!/bin/sh\n"+run+"\n"), 0o755)
	}
	checkWarnsByDefault(t, "--output-dir", dir+"/out", dir+"/w", dir+"/n")
}
