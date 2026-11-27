package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `slinitctl cat` prints what the daemon loaded the service from and
// what applies on top of it: for a systemd unit, its drop-ins and the
// slinit description it reads as; for a slinit file, its .override.
func TestCatShowsTheServiceFiles(t *testing.T) {
	set, conn := editTestDaemon(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "web.service"), []byte("[Service]\nExecStart=/usr/bin/web\nMemoryMax=1G\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "web.service.d"), 0o755)
	os.WriteFile(filepath.Join(dir, "web.service.d", "override.conf"), []byte("[Service]\nExecStart=\nExecStart=/usr/local/bin/web\n"), 0o644)
	addEditTestService(set, "web", dir)

	var out bytes.Buffer
	if err := cmdCat(conn, []string{"web"}, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"# " + filepath.Join(dir, "web.service") + "\n[Service]\nExecStart=/usr/bin/web\n",
		"# " + filepath.Join(dir, "web.service.d", "override.conf") + "\n",
		"# slinit reads it as:\n",
		"command = /usr/local/bin/web\n",
		"cgroup-memory-max = 1073741824\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
	if strings.Index(got, "override.conf") > strings.Index(got, "slinit reads it as") {
		t.Errorf("the drop-in comes after the translation:\n%s", got)
	}

	os.WriteFile(filepath.Join(dir, "plain"), []byte("type = internal\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "plain.override"), []byte("description = x\n"), 0o644)
	addEditTestService(set, "plain", dir)
	out.Reset()
	if err := cmdCat(conn, []string{"plain"}, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "type = internal\n") || !strings.Contains(got, "plain.override\ndescription = x\n") {
		t.Errorf("slinit file and its override:\n%s", got)
	}
}
