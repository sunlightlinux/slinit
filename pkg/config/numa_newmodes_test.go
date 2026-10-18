package config

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// preferred-many and weighted-interleave, added for systemd v262
// parity. golang.org/x/sys/unix does not name them, so the values are
// slinit's own constants and a wrong one would select a different
// policy than the service asked for — the test pins them against the
// kernel enum's ordering.
func TestNewMempolicyModesMatchTheKernelEnum(t *testing.T) {
	cases := map[string]uint32{
		"default":             0,
		"preferred":           1,
		"bind":                2,
		"interleave":          3,
		"local":               4,
		"preferred-many":      5,
		"weighted-interleave": 6,
	}
	for name, want := range cases {
		got, err := parseMempolicyMode(name)
		if err != nil {
			t.Errorf("parseMempolicyMode(%q): %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("parseMempolicyMode(%q) = %d, want %d", name, got, want)
		}
	}
}

// An unknown mode must still be refused, and the message should name
// the new modes — an operator who typed one of them slightly wrong
// learns the vocabulary from the error.
func TestUnknownMempolicyModeIsRefusedAndListsTheNewModes(t *testing.T) {
	_, err := parseMempolicyMode("weighted")
	if err == nil {
		t.Fatal("parseMempolicyMode(\"weighted\") was accepted")
	}
	for _, want := range []string{"preferred-many", "weighted-interleave"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}

// The names are only half of it: both new modes take a nodemask, so a
// service naming one without numa-nodes has to be refused at load
// time. Classifying them with default/local instead would have let a
// policy through with nothing to apply it to.
func TestNewMempolicyModesRequireNumaNodes(t *testing.T) {
	for _, mode := range []string{"preferred-many", "weighted-interleave"} {
		dir := t.TempDir()
		writeServiceFile(t, dir, "svc",
			"type = process\ncommand = /bin/sleep 60\nnuma-mempolicy = "+mode+"\n")

		ss := service.NewServiceSet(&testReloadLogger{})
		loader := NewDirLoader(ss, []string{dir})
		ss.SetLoader(loader)

		if _, err := loader.LoadService("svc"); err == nil {
			t.Errorf("numa-mempolicy = %s loaded with no numa-nodes; want a load error", mode)
		}
	}
}

// And with a nodemask they must load, or the directive would be
// unusable rather than merely strict.
func TestNewMempolicyModesLoadWithNumaNodes(t *testing.T) {
	for _, mode := range []string{"preferred-many", "weighted-interleave"} {
		dir := t.TempDir()
		writeServiceFile(t, dir, "svc",
			"type = process\ncommand = /bin/sleep 60\n"+
				"numa-mempolicy = "+mode+"\nnuma-nodes = 0\n")

		ss := service.NewServiceSet(&testReloadLogger{})
		loader := NewDirLoader(ss, []string{dir})
		ss.SetLoader(loader)

		if _, err := loader.LoadService("svc"); err != nil {
			t.Errorf("numa-mempolicy = %s with numa-nodes = 0: %v", mode, err)
		}
	}
}
