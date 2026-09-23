package metrics

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

func testSet(t *testing.T) *service.ServiceSet {
	t.Helper()
	return service.NewServiceSet(logging.New(logging.LevelError))
}

// sample returns the value of one line, or "" when the series is absent.
func sample(out, series string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, series+" ") {
			return strings.TrimPrefix(line, series+" ")
		}
	}
	return ""
}

// The exposition format is a contract with whatever scrapes it, and the
// parts a scraper actually rejects are the ones worth pinning: every
// series needs its TYPE, every sample needs a numeric value, and a
// counter must not be given a name that ends in anything else.
func TestRenderIsValidExposition(t *testing.T) {
	ss := testSet(t)
	svc := service.NewInternalService(ss, "boot")
	ss.AddService(svc)

	var b strings.Builder
	if err := Render(&b, ss, "2.4.0-test"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := b.String()

	declared := map[string]string{} // series -> type
	for _, line := range strings.Split(out, "\n") {
		switch {
		case line == "":
		case strings.HasPrefix(line, "# TYPE "):
			f := strings.Fields(line)
			if len(f) != 4 {
				t.Errorf("malformed TYPE line: %q", line)
				continue
			}
			declared[f[2]] = f[3]
		case strings.HasPrefix(line, "# HELP "):
		case strings.HasPrefix(line, "#"):
			t.Errorf("unknown comment line: %q", line)
		default:
			// "name{labels} value" or "name value"
			sp := strings.LastIndexByte(line, ' ')
			if sp < 0 {
				t.Errorf("sample without a value: %q", line)
				continue
			}
			name := line[:sp]
			if i := strings.IndexByte(name, '{'); i >= 0 {
				name = name[:i]
			}
			if _, ok := declared[name]; !ok {
				t.Errorf("sample %q has no # TYPE line", name)
			}
			value := line[sp+1:]
			if strings.TrimLeft(value, "0123456789.+-eE") != "" {
				t.Errorf("non-numeric value in %q", line)
			}
		}
	}

	for name, typ := range declared {
		if strings.HasSuffix(name, "_total") && typ != "counter" {
			t.Errorf("%s ends in _total but is declared %s", name, typ)
		}
		if typ == "counter" && !strings.HasSuffix(name, "_total") {
			t.Errorf("counter %s should end in _total", name)
		}
	}
}

func TestRenderReportsTheServiceSet(t *testing.T) {
	ss := testSet(t)
	boot := service.NewInternalService(ss, "boot")
	ss.AddService(boot)
	ss.AddService(service.NewInternalService(ss, "worker"))

	var b strings.Builder
	if err := Render(&b, ss, "2.4.0-test"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := b.String()

	if !strings.Contains(out, `slinit_build_info{version="2.4.0-test"} 1`) {
		t.Error("build_info missing or mislabelled")
	}
	if got := sample(out, `slinit_services{state="stopped"}`); got != "2" {
		t.Errorf(`services{state="stopped"} = %q, want 2`, got)
	}
	if got := sample(out, `slinit_service_up{service="worker"}`); got != "0" {
		t.Errorf("worker should not be up yet, got %q", got)
	}
	if got := sample(out, "slinit_boot_ready"); got != "0" {
		t.Errorf("boot_ready = %q before the boot target starts, want 0", got)
	}
	// Per-service series are the point of the endpoint; both services
	// must appear, sorted, so the output is stable between scrapes.
	if strings.Index(out, `slinit_service_up{service="boot"}`) >
		strings.Index(out, `slinit_service_up{service="worker"}`) {
		t.Error("service series are not sorted by name")
	}
}

// A service name comes from a file name, so it can contain the two
// characters the format reserves. Emitting them raw produces a line no
// scraper can parse, and the whole scrape is dropped, not just that
// series.
func TestRenderEscapesLabelValues(t *testing.T) {
	ss := testSet(t)
	ss.AddService(service.NewInternalService(ss, `od"d\name`))

	var b strings.Builder
	if err := Render(&b, ss, `v"1`); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := b.String()

	if !strings.Contains(out, `slinit_service_up{service="od\"d\\name"} 0`) {
		t.Errorf("service label not escaped; got:\n%s", out)
	}
	if !strings.Contains(out, `slinit_build_info{version="v\"1"} 1`) {
		t.Error("version label not escaped")
	}
}
