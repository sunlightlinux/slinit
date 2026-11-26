package config

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// Start hooks are wired into process services only. On a scripted
// service they never ran and nothing said so; loading one now warns.
func TestStartHooksOnNonProcessTypeWarn(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "oneshot",
		"type = scripted\ncommand = /bin/true\npre-start-command = /bin/false\n")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)
	_, loadErr := loader.LoadService("oneshot")
	os.Stderr = old
	w.Close()
	out, _ := io.ReadAll(r)
	if loadErr != nil {
		t.Fatalf("load failed: %v", loadErr)
	}
	if !strings.Contains(string(out), "pre-start-command/post-start-command are only supported for type = process") {
		t.Errorf("no warning for an ignored pre-start-command; stderr: %q", out)
	}
}
