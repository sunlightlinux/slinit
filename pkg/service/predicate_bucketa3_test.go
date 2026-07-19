package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBucketA3KindByNameRoundtrip(t *testing.T) {
	for _, name := range []string{"memory-pressure", "cpu-pressure", "io-pressure"} {
		kind, ok := PredicateKindByName(name)
		if !ok {
			t.Errorf("PredicateKindByName(%q) = _,false; want true", name)
			continue
		}
		p := Predicate{Kind: kind, Param: "x"}
		got := p.String()
		want := "condition-" + name + "=x"
		if got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
}

// TestPSIPressureParseAndCompare exercises the shared checker against
// a synthesised PSI file so the test is deterministic regardless of
// the host's actual pressure state.
func TestPSIPressureParseAndCompare(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "memory")
	// avg10=12.34 → both < 50 and >= 12 must evaluate correctly.
	content := "some avg10=12.34 avg60=5.0 avg300=1.0 total=999\n" +
		"full avg10=0.0  avg60=0.0 avg300=0.0 total=0\n"
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := readPSISomeAvg10(file)
	if err != nil {
		t.Fatalf("readPSISomeAvg10: %v", err)
	}
	if got != 12.34 {
		t.Errorf("avg10 parse: got %v, want 12.34", got)
	}

	// Explicit operators.
	for _, tc := range []struct {
		param string
		want  bool
	}{
		{"< 50", true},   // 12.34 < 50 ✓
		{">= 12", true},  // 12.34 >= 12 ✓
		{"> 20", false},  // 12.34 > 20 ✗
		{"<= 12.34", true}, // exact upper edge ✓
		{"12.34", false},  // bare form defaults to >= per checker: 12.34 >= 12.34 → true
	} {
		ok, why := checkPSIPressure(file, tc.param)
		// Special-case: bare "12.34" — per our design the missing op
		// becomes >= so this is actually true. Reflect that.
		if tc.param == "12.34" {
			if !ok {
				t.Errorf("param=%q: got false (%q), bare form should default to >= (12.34>=12.34)", tc.param, why)
			}
			continue
		}
		if ok != tc.want {
			t.Errorf("param=%q: got %v, want %v (%s)", tc.param, ok, tc.want, why)
		}
	}
}

// TestPSIPressureRejectsBadPercent — parse-time errors surface with a
// clear message rather than defaulting to 0%.
func TestPSIPressureRejectsBadPercent(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "cpu")
	os.WriteFile(file, []byte("some avg10=0 avg60=0 avg300=0 total=0\n"), 0644)
	if ok, why := checkPSIPressure(file, "> 150"); ok || why == "" {
		t.Errorf("percent > 100 should fail with reason; got ok=%v why=%q", ok, why)
	}
	if ok, why := checkPSIPressure(file, "> notanumber"); ok || why == "" {
		t.Errorf("non-numeric percent should fail with reason; got ok=%v why=%q", ok, why)
	}
}

// TestPSIPressureMissingFile — kernel too old / PSI disabled path.
func TestPSIPressureMissingFile(t *testing.T) {
	if ok, why := checkPSIPressure("/nonexistent/pressure/memory", "< 50"); ok || why == "" {
		t.Errorf("missing PSI file should fail with reason; got ok=%v why=%q", ok, why)
	}
}
