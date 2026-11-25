package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// A relative depends-on.d / depends-ms.d / waits-for.d / prepared-by.d
// is resolved against the directory of the file that holds the setting,
// as dinit does (input_stack.current_file_name()) — an @include'd
// fragment's directory, not the main service file's. A setting in the
// main file keeps resolving against the main file's directory.
func TestDirDepsResolveAgainstIncludingFragment(t *testing.T) {
	for _, c := range []struct {
		setting string
		depType service.DependencyType
	}{
		{"depends-on.d", service.DepRegular},
		{"depends-ms.d", service.DepMilestone},
		{"waits-for.d", service.DepWaitsFor},
		{"prepared-by.d", service.DepPreparedBy},
	} {
		t.Run(c.setting, func(t *testing.T) {
			root := t.TempDir()
			svcDir := filepath.Join(root, "services")
			frag := filepath.Join(root, "frag")
			for _, d := range []string{filepath.Join(frag, "wd"), filepath.Join(svcDir, "main.d")} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			writeServiceFile(t, frag, "common", c.setting+" = wd\n")
			writeServiceFile(t, svcDir, "s", "type = internal\n@include "+
				filepath.Join(frag, "common")+"\n"+c.setting+" = main.d\n")
			writeServiceFile(t, svcDir, "fromfrag", "type = internal\n")
			writeServiceFile(t, svcDir, "frommain", "type = internal\n")
			writeServiceFile(t, filepath.Join(frag, "wd"), "fromfrag", "")
			writeServiceFile(t, filepath.Join(svcDir, "main.d"), "frommain", "")

			ss := service.NewServiceSet(&testReloadLogger{})
			l := NewDirLoader(ss, []string{svcDir})
			ss.SetLoader(l)
			svc, err := l.LoadService("s")
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			got := map[string]bool{}
			for _, d := range svc.Record().Dependencies() {
				if d.DepType == c.depType {
					got[d.To.Name()] = true
				}
			}
			if !got["fromfrag"] || !got["frommain"] || len(got) != 2 {
				t.Errorf("deps %v, want fromfrag (fragment dir) and frommain (main dir)", got)
			}
		})
	}
}

// A conf.d overlay is another file: its relative waits-for.d is its own
// directory's, and the record hands enable that same resolved path.
func TestWaitsForDResolvesAgainstOverlayDir(t *testing.T) {
	root := t.TempDir()
	svcDir := filepath.Join(root, "services")
	confD := filepath.Join(root, "conf.d")
	for _, d := range []string{svcDir, filepath.Join(confD, "wd")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeServiceFile(t, svcDir, "s", "type = internal\n")
	writeServiceFile(t, svcDir, "dep", "type = internal\n")
	writeServiceFile(t, confD, "s", "waits-for.d = wd\n")
	writeServiceFile(t, filepath.Join(confD, "wd"), "dep", "")

	ss := service.NewServiceSet(&testReloadLogger{})
	l := NewDirLoader(ss, []string{svcDir})
	l.SetOverlayDirs([]string{confD})
	ss.SetLoader(l)
	svc, err := l.LoadService("s")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	deps := svc.Record().Dependencies()
	if len(deps) != 1 || deps[0].To.Name() != "dep" {
		t.Errorf("deps %d, want dep from the overlay's wd/", len(deps))
	}
	want := filepath.Join(confD, "wd")
	if got := svc.Record().WaitsForDirs(); len(got) != 1 || got[0] != want {
		t.Errorf("WaitsForDirs %v, want [%s]", got, want)
	}
}
