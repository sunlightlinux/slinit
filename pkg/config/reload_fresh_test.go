package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// A reload must leave a service exactly as a fresh load of the same file
// would. It used to keep whatever the old file set and the new one left
// out: the loader applied most settings only when present, so dropping
// run-as kept the old user, dropping supplementary-groups or
// capabilities kept those privileges, dropping a seccomp or sandbox
// setting kept the restriction, and dropping restart-delay or
// start-timeout kept the old timing — 37 fields in all, found by this
// test (the finit equivalent was finit bf42811a). Every service file the
// functional and acceptance suites use is the input, three ways: rich →
// minimal shows what is left behind, minimal → rich what is missed, and
// rich → rich what accumulates.

// depKeys are the lines dropped from the corpus files: they name other
// services or files that do not exist in a scratch directory.
var depKeys = regexp.MustCompile(`^\s*(depends-on|depends-ms|waits-for|before|after|consumer-of|shared-logger|chain-to|requires|wants|part-of|binds-to|conflicts|socket-listen|socket-activation|env-file|env-dir|logfile|log-type|output-logger|error-logger)(\.d)?\b`)

var typeLine = regexp.MustCompile(`^\s*type\s*=\s*(\S+)`)

// runtimeFields are state, not configuration: a reload has no business
// making them equal, and a fresh record has its own.
var runtimeFields = map[string]bool{
	"serviceDir": true, // where the file was found; same dir here, kept for safety
	// extraEnv mixes `slinitctl setenv` values with load-options'
	// DINIT_SERVICE/USER/HOME. A reload cannot tell them apart, and
	// clearing an operator's setenv on reload would be a regression, so
	// a dropped load-option's variables stay until reset-env.
	"extraEnv": true,
}

func minimalConfig(t service.ServiceType, src string) string {
	out := ""
	for _, l := range strings.Split(src, "\n") {
		if m := typeLine.FindStringSubmatch(l); m != nil {
			out = "type = " + m[1] + "\n"
		}
	}
	switch t {
	case service.TypeProcess, service.TypeScripted:
		out += "command = /bin/true\n"
	case service.TypeBGProcess:
		out += "command = /bin/true\n"
		// pid-file may not change on a started bgprocess; keep it so
		// the pair stays loadable the same way.
		for _, l := range strings.Split(src, "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), "pid-file") {
				out += l + "\n"
			}
		}
	}
	return out
}

func loadFrom(t *testing.T, dir string) (*DirLoader, service.Service, error) {
	t.Helper()
	ss := service.NewServiceSet(&testReloadLogger{})
	l := NewDirLoader(ss, []string{dir})
	ss.SetLoader(l)
	svc, err := l.LoadService("s")
	return l, svc, err
}

// diffFields compares the configuration fields of two services of the
// same concrete type, descending into embedded structs (ServiceRecord).
func diffFields(a, b reflect.Value, out map[string]bool) {
	for i := 0; i < a.NumField(); i++ {
		f := a.Type().Field(i)
		av, bv := a.Field(i), b.Field(i)
		if f.Anonymous && av.Kind() == reflect.Struct {
			diffFields(av, bv, out)
			continue
		}
		if runtimeFields[f.Name] {
			continue
		}
		switch av.Kind() {
		case reflect.Chan, reflect.Func, reflect.UnsafePointer, reflect.Interface:
			continue
		case reflect.Ptr:
			// Pointers to scalars (nice, oom-score-adj, umask) are
			// configuration; pointers to structs are live objects
			// (the service set, loggers, checkers, timers).
			if f.Type.Elem().Kind() == reflect.Struct {
				continue
			}
		}
		tn := f.Type.String()
		if strings.Contains(tn, "sync.") || strings.Contains(tn, "atomic") || strings.Contains(tn, "time.Time") {
			continue
		}
		if av.Kind() == reflect.Ptr {
			if av.IsNil() != bv.IsNil() || (!av.IsNil() && fmt.Sprint(av.Elem()) != fmt.Sprint(bv.Elem())) {
				out[f.Name] = true
			}
			continue
		}
		if fmt.Sprint(av) != fmt.Sprint(bv) {
			out[f.Name] = true
		}
	}
}

func TestReloadEqualsFreshLoad(t *testing.T) {
	var files []string
	for _, g := range []string{"../../tests/*/cases/*.d/*", "../../tests/*/*/cases/*.d/*"} {
		m, _ := filepath.Glob(g)
		files = append(files, m...)
	}
	if len(files) < 100 {
		t.Fatalf("found only %d corpus service files; the glob is wrong", len(files))
	}

	stale := map[string][]string{}
	pairs := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil || len(b) == 0 {
			continue
		}
		var keep []string
		for _, l := range strings.Split(string(b), "\n") {
			if !depKeys.MatchString(l) {
				keep = append(keep, l)
			}
		}
		rich := strings.Join(keep, "\n")
		name := filepath.Base(filepath.Dir(f)) + "/" + filepath.Base(f)

		dir := t.TempDir()
		path := filepath.Join(dir, "s")
		if err := os.WriteFile(path, []byte(rich), 0o644); err != nil {
			t.Fatal(err)
		}
		_, probe, err := loadFrom(t, dir)
		if err != nil {
			continue // needs something the scratch dir lacks
		}
		minimal := minimalConfig(probe.Type(), rich)

		for _, dirn := range []struct{ from, to, label string }{
			{rich, minimal, "rich→minimal"},
			{minimal, rich, "minimal→rich"},
			// Reloading an unchanged file must change nothing: this is
			// what catches a setting that accumulates (rlimits were
			// appended once more on every reload).
			{rich, rich, "rich→rich"},
		} {
			os.WriteFile(path, []byte(dirn.from), 0o644)
			l, svc, err := loadFrom(t, dir)
			if err != nil {
				continue
			}
			os.WriteFile(path, []byte(dirn.to), 0o644)
			got, err := l.ReloadService(svc)
			if err != nil || got != svc {
				continue // a type change replaces the record; not this test
			}
			_, fresh, err := loadFrom(t, dir)
			if err != nil {
				continue
			}
			pairs++
			d := map[string]bool{}
			diffFields(reflect.ValueOf(svc).Elem(), reflect.ValueOf(fresh).Elem(), d)
			for k := range d {
				stale[k] = append(stale[k], dirn.label+" "+name)
			}
		}
	}
	if pairs < 300 {
		t.Fatalf("only %d reload pairs compared; most corpus files failed to load", pairs)
	}
	keys := make([]string, 0, len(stale))
	for k := range stale {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ex := stale[k]
		t.Errorf("after reload %s differs from a fresh load (%d cases, e.g. %s)", k, len(ex), ex[0])
	}
	t.Logf("%d reload pairs compared", pairs)
}
