package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/config"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// The boot-failure menu's first line said "No service files found"
// for every load failure, a boot file with a syntax error included.
// The errors here come from the real loader, not built by hand.
func TestBootFailureHeadline(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("bad-syntax", "type = internal\nthis line is not a setting\n")
	write("missing-dep", "type = internal\ndepends-on: nowhere\n")

	load := func(name string) bootLoadErr {
		set := service.NewServiceSet(quietSvcLogger{})
		set.SetLoader(config.NewDirLoader(set, []string{dir}))
		_, err := set.LoadService(name)
		if err == nil {
			t.Fatalf("%s: loaded, want a load error", name)
		}
		return bootLoadErr{name, err}
	}
	dirs := []string{dir}
	notFound := fmt.Sprintf("No service files found in %v", dirs)

	cases := []struct {
		what string
		errs []bootLoadErr
		want string
	}{
		{"boot file absent", []bootLoadErr{load("boot")}, notFound},
		{"every boot service absent", []bootLoadErr{load("boot"), load("other")}, notFound},
		{"syntax error", []bootLoadErr{load("bad-syntax")}, "failed to load"},
		{"dependency absent", []bootLoadErr{load("missing-dep")}, "failed to load"},
		{"one absent, one broken", []bootLoadErr{load("boot"), load("bad-syntax")}, "failed to load"},
	}
	for _, c := range cases {
		got := bootFailureHeadline(dirs, c.errs)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: headline %q, want it to contain %q", c.what, got, c.want)
		}
	}
}
