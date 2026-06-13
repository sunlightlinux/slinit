package service

import (
	"os"
	"path/filepath"
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

func TestInternalServiceSkipOnConditionFail(t *testing.T) {
	// InternalService doesn't run a BringUp predicate check, so use
	// scripted-with-no-start-command path? Easier: reach into a
	// real-ish flow via internal. Predicates only fire from BringUp
	// of process/scripted/bgprocess.
	t.Skip("predicates only fire from process/scripted/bgprocess BringUp; covered by record_test.go-style flows below")
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
