package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- evaluator unit tests (don't need a ServiceSet) ---

func TestPredicatePathExists(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "exists")
	if err := os.WriteFile(file, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	ok, _ := (Predicate{Kind: PredPathExists, Param: file}).Evaluate()
	if !ok {
		t.Error("existing file: expected ok")
	}
	ok, _ = (Predicate{Kind: PredPathExists, Param: filepath.Join(dir, "nope")}).Evaluate()
	if ok {
		t.Error("missing file: expected !ok")
	}
}

func TestPredicateNegation(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "exists")
	if err := os.WriteFile(file, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	// Negated predicate over an existing file → must fail
	ok, _ := (Predicate{Kind: PredPathExists, Param: file, Negate: true}).Evaluate()
	if ok {
		t.Error("negated existing: expected !ok")
	}
	// Negated predicate over a missing file → must succeed
	ok, _ = (Predicate{Kind: PredPathExists, Param: filepath.Join(dir, "nope"), Negate: true}).Evaluate()
	if !ok {
		t.Error("negated missing: expected ok")
	}
}

func TestPredicatePathIsDirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	os.WriteFile(file, []byte("x"), 0644)

	ok, _ := (Predicate{Kind: PredPathIsDirectory, Param: dir}).Evaluate()
	if !ok {
		t.Error("dir: expected ok")
	}
	ok, _ = (Predicate{Kind: PredPathIsDirectory, Param: file}).Evaluate()
	if ok {
		t.Error("regular file as dir: expected !ok")
	}
}

func TestPredicateFileNotEmpty(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	full := filepath.Join(dir, "full")
	os.WriteFile(empty, nil, 0644)
	os.WriteFile(full, []byte("data"), 0644)

	ok, _ := (Predicate{Kind: PredFileNotEmpty, Param: full}).Evaluate()
	if !ok {
		t.Error("non-empty file: expected ok")
	}
	ok, _ = (Predicate{Kind: PredFileNotEmpty, Param: empty}).Evaluate()
	if ok {
		t.Error("empty file: expected !ok")
	}
}

func TestPredicateDirectoryNotEmpty(t *testing.T) {
	dir := t.TempDir()
	sub := t.TempDir()

	ok, _ := (Predicate{Kind: PredDirectoryNotEmpty, Param: dir}).Evaluate()
	if ok {
		t.Error("empty dir: expected !ok")
	}
	if err := os.WriteFile(filepath.Join(sub, "x"), []byte("y"), 0644); err != nil {
		t.Fatal(err)
	}
	ok, _ = (Predicate{Kind: PredDirectoryNotEmpty, Param: sub}).Evaluate()
	if !ok {
		t.Error("non-empty dir: expected ok")
	}
}

func TestPredicatePathExistsGlob(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "match-1"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "match-2"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	ok, _ := (Predicate{Kind: PredPathExistsGlob, Param: filepath.Join(dir, "match-*")}).Evaluate()
	if !ok {
		t.Error("glob with matches: expected ok")
	}
	ok, _ = (Predicate{Kind: PredPathExistsGlob, Param: filepath.Join(dir, "miss-*")}).Evaluate()
	if ok {
		t.Error("glob without matches: expected !ok")
	}
}

// --- CheckPredicates outcome tests ---

func TestCheckPredicatesEmpty(t *testing.T) {
	rec := &ServiceRecord{}
	out, _ := rec.CheckPredicates()
	if out != PredOK {
		t.Errorf("no predicates: outcome=%v want OK", out)
	}
}

func TestCheckPredicatesConditionSkipsSilently(t *testing.T) {
	rec := &ServiceRecord{}
	rec.SetPredicates([]Predicate{
		{Kind: PredPathExists, Param: "/nonexistent/skip-path"},
	})
	out, reason := rec.CheckPredicates()
	if out != PredSkip {
		t.Errorf("condition fail: outcome=%v want Skip", out)
	}
	if reason == "" {
		t.Error("skip reason should be populated")
	}
}

func TestCheckPredicatesAssertFails(t *testing.T) {
	rec := &ServiceRecord{}
	rec.SetPredicates([]Predicate{
		{Kind: PredPathExists, Param: "/nonexistent/assert-path", IsAssert: true},
	})
	out, _ := rec.CheckPredicates()
	if out != PredFailed {
		t.Errorf("assert fail: outcome=%v want Failed", out)
	}
}

func TestCheckPredicatesAssertOutranksCondition(t *testing.T) {
	// A failing condition followed by a failing assert must surface
	// the assert (the more severe outcome) so dependents cascade-fail.
	rec := &ServiceRecord{}
	rec.SetPredicates([]Predicate{
		{Kind: PredPathExists, Param: "/nonexistent/condition"},
		{Kind: PredPathExists, Param: "/nonexistent/assert", IsAssert: true},
	})
	out, _ := rec.CheckPredicates()
	if out != PredFailed {
		t.Errorf("assert+condition: outcome=%v want Failed", out)
	}
}

// --- Integration: condition-* skip path through ServiceSet ---

// Since 3.7.0 internal and triggered services evaluate predicates like
// every other type. They used to ignore them without a word; this test
// was a t.Skip saying so.
func TestInternalAndTriggeredHonourPredicates(t *testing.T) {
	missing := "/nonexistent/predicate-internal-test"
	type mk func(*ServiceSet, string) Service
	internal := func(set *ServiceSet, n string) Service { return NewInternalService(set, n) }
	triggered := func(set *ServiceSet, n string) Service { return NewTriggeredService(set, n) }
	for _, tc := range []struct {
		name    string
		make    mk
		assert  bool
		pass    bool // the predicate holds
		want    ServiceState
		skipped bool
	}{
		{"internal, condition false: skipped", internal, false, false, StateStarted, true},
		{"internal, assert false: fails", internal, true, false, StateStopped, false},
		{"internal, condition true: starts", internal, false, true, StateStarted, false},
		// Untriggered, so without the skip it would wait in STARTING.
		{"triggered, condition false: skipped without a trigger", triggered, false, false, StateStarted, true},
		{"triggered, assert false: fails", triggered, true, false, StateStopped, false},
		{"triggered, condition true: waits for its trigger", triggered, false, true, StateStarting, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set, _ := newTestSet()
			svc := tc.make(set, "svc")
			set.AddService(svc)
			param := missing
			if tc.pass {
				param = "/"
			}
			svc.Record().SetPredicates([]Predicate{{Kind: PredPathExists, Param: param, IsAssert: tc.assert}})
			set.StartService(svc)
			if got := svc.State(); got != tc.want {
				t.Errorf("state = %v, want %v", got, tc.want)
			}
			if got := svc.Record().WasStartSkipped(); got != tc.skipped {
				t.Errorf("WasStartSkipped = %v, want %v", got, tc.skipped)
			}
		})
	}
}

// TestScriptedServiceConditionSkip simulates a scripted service whose
// condition-* fails and verifies the service reaches STARTED with
// WasStartSkipped() = true and no process having been forked.
func TestScriptedServiceConditionSkip(t *testing.T) {
	set, logger := newTestSet()

	svc := NewScriptedService(set, "svc")
	svc.SetStartCommand([]string{"/bin/true"})
	set.AddService(svc)

	svc.Record().SetPredicates([]Predicate{
		{Kind: PredPathExists, Param: "/nonexistent/condition-skip-test"},
	})

	set.StartService(svc)

	if svc.State() != StateStarted {
		t.Errorf("expected STARTED via skip, got %v", svc.State())
	}
	if !svc.Record().WasStartSkipped() {
		t.Error("WasStartSkipped should be true after condition fail")
	}
	// Dependents would also see this service started → logger should
	// have recorded it.
	found := false
	for _, n := range logger.started {
		if n == "svc" {
			found = true
		}
	}
	if !found {
		t.Errorf("logger should have recorded started=%v", logger.started)
	}
}

func TestScriptedServiceAssertFails(t *testing.T) {
	set, _ := newTestSet()

	svc := NewScriptedService(set, "svc-assert")
	svc.SetStartCommand([]string{"/bin/true"})
	set.AddService(svc)

	svc.Record().SetPredicates([]Predicate{
		{Kind: PredPathExists, Param: "/nonexistent/assert", IsAssert: true},
	})

	set.StartService(svc)

	if svc.State() != StateStopped {
		t.Errorf("expected STOPPED via assert fail, got %v", svc.State())
	}
	if !svc.Record().DidStartFail() {
		t.Error("DidStartFail should be true after assert fail")
	}
}

// Triggering checks: every plain one must hold, and at least one of the
// triggering ones of the same list, as systemd's condition_test_list.
func TestCombineTriggeringPredicates(t *testing.T) {
	c := func(trigger, assert bool) Predicate {
		return Predicate{Kind: PredPathExists, Param: "/x", Trigger: trigger, IsAssert: assert}
	}
	for _, tc := range []struct {
		name  string
		preds []Predicate
		ok    []bool
		want  PredicateOutcome
	}{
		{"one trigger holds", []Predicate{c(true, false), c(true, false)}, []bool{false, true}, PredOK},
		{"no trigger holds", []Predicate{c(true, false), c(true, false)}, []bool{false, false}, PredSkip},
		{"a plain one fails, a trigger holds", []Predicate{c(false, false), c(true, false)}, []bool{false, true}, PredSkip},
		{"triggers per list", []Predicate{c(true, false), c(true, true)}, []bool{true, false}, PredFailed},
		{"no triggers, all hold", []Predicate{c(false, false)}, []bool{true}, PredOK},
	} {
		res := make([]predResult, len(tc.ok))
		for i, ok := range tc.ok {
			res[i] = predResult{ok: ok, why: "x"}
		}
		if got, why := combinePredicates(tc.preds, res); got != tc.want {
			t.Errorf("%s: %v (%s), want %v", tc.name, got, why, tc.want)
		}
	}
}

func TestConditionCapability(t *testing.T) {
	status := "Name:\tx\nCapBnd:\t0000000000200000\n" // only bit 21, CAP_SYS_ADMIN
	if in, err := capInBoundingSet(status, 21); err != nil || !in {
		t.Errorf("bit 21: %v %v", in, err)
	}
	if in, _ := capInBoundingSet(status, 0); in {
		t.Error("CAP_CHOWN reported in a set without it")
	}
	if _, err := capInBoundingSet("Name:\tx\n", 0); err == nil {
		t.Error("a status without CapBnd answered")
	}
	// This process's own set: the answer and its negation disagree.
	p := Predicate{Kind: PredCapability, Param: "CAP_CHOWN"}
	ok, _ := p.Evaluate()
	p.Negate = true
	nok, _ := p.Evaluate()
	if ok == nok {
		t.Errorf("CAP_CHOWN %v, negated %v", ok, nok)
	}
	if ok, why := (Predicate{Kind: PredCapability, Param: "CAP_NOPE"}).Evaluate(); ok || !strings.Contains(why, "not one capability") {
		t.Errorf("a bad name: %v %q", ok, why)
	}
}
