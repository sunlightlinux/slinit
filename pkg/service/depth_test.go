package service

import (
	"fmt"
	"testing"
)

type depTestLogger struct{}

func (depTestLogger) ServiceStarted(string)        {}
func (depTestLogger) ServiceStopped(string)        {}
func (depTestLogger) ServiceFailed(string, bool)   {}
func (depTestLogger) Error(string, ...interface{}) {}
func (depTestLogger) Info(string, ...interface{})  {}

func newDepTestSet() *ServiceSet {
	return NewServiceSet(depTestLogger{})
}

func TestCalcDepth(t *testing.T) {
	set := newDepTestSet()
	a := NewInternalService(set, "a")
	b := NewInternalService(set, "b")
	c := NewInternalService(set, "c")

	// a depends on b, b depends on c
	a.Record().AddDep(b, DepRegular)
	b.Record().AddDep(c, DepRegular)

	// c has no deps → depth 0
	if d := calcDepth(c.Record()); d != 0 {
		t.Fatalf("c depth: got %d, want 0", d)
	}
	// b depends on c(depth=0) → depth 1
	c.Record().SetDepDepth(0)
	if d := calcDepth(b.Record()); d != 1 {
		t.Fatalf("b depth: got %d, want 1", d)
	}
	// a depends on b(depth=1) → depth 2
	b.Record().SetDepDepth(1)
	if d := calcDepth(a.Record()); d != 2 {
		t.Fatalf("a depth: got %d, want 2", d)
	}
}

func TestDepDepthUpdaterPropagates(t *testing.T) {
	set := newDepTestSet()
	a := NewInternalService(set, "a")
	b := NewInternalService(set, "b")
	c := NewInternalService(set, "c")

	// Chain: a → b → c
	a.Record().AddDep(b, DepRegular)
	b.Record().AddDep(c, DepRegular)

	// Set initial depths correctly
	c.Record().SetDepDepth(0)
	b.Record().SetDepDepth(1)
	a.Record().SetDepDepth(2)

	// Now add d as dep of c, depths should propagate up
	d := NewInternalService(set, "d")
	c.Record().AddDep(d, DepRegular)

	var updater DepDepthUpdater
	updater.AddPotentialUpdate(c)
	if err := updater.ProcessUpdates(); err != nil {
		t.Fatal(err)
	}
	updater.Commit()

	if c.Record().DepDepth() != 1 {
		t.Errorf("c depth: got %d, want 1", c.Record().DepDepth())
	}
	if b.Record().DepDepth() != 2 {
		t.Errorf("b depth: got %d, want 2", b.Record().DepDepth())
	}
	if a.Record().DepDepth() != 3 {
		t.Errorf("a depth: got %d, want 3", a.Record().DepDepth())
	}
}

func TestDepDepthUpdaterRollback(t *testing.T) {
	set := newDepTestSet()
	a := NewInternalService(set, "a")
	b := NewInternalService(set, "b")

	a.Record().AddDep(b, DepRegular)
	a.Record().SetDepDepth(1)
	b.Record().SetDepDepth(0)

	// Manually set a bogus depth then rollback
	var updater DepDepthUpdater
	updater.AddPotentialUpdate(a)
	a.Record().SetDepDepth(99) // simulate a change
	updater.Rollback()

	if a.Record().DepDepth() != 1 {
		t.Errorf("after rollback: got %d, want 1", a.Record().DepDepth())
	}
}

func TestDepDepthUpdaterMaxExceeded(t *testing.T) {
	set := newDepTestSet()

	// Build a chain of MaxDepDepth+2 services
	svcs := make([]Service, MaxDepDepth+2)
	for i := range svcs {
		svcs[i] = NewInternalService(set, string(rune('A'+i)))
	}
	for i := 0; i < len(svcs)-1; i++ {
		svcs[i].Record().AddDep(svcs[i+1], DepRegular)
	}

	// Set depths bottom-up
	for i := len(svcs) - 1; i >= 0; i-- {
		svcs[i].Record().SetDepDepth(len(svcs) - 1 - i)
	}

	// Adding one more should exceed max
	extra := NewInternalService(set, "extra")
	svcs[len(svcs)-1].Record().AddDep(extra, DepRegular)

	var updater DepDepthUpdater
	updater.AddPotentialUpdate(svcs[len(svcs)-1])
	err := updater.ProcessUpdates()
	if err == nil {
		t.Fatal("expected error for exceeding MaxDepDepth")
	}
	updater.Rollback()
}

// In a diamond (a -> b, a -> c -> b) a is reached through b before c
// has its new depth; a must be recomputed once c changes, or its depth
// stays short and the MaxDepDepth check can be walked past.
func TestDepDepthUpdaterDiamond(t *testing.T) {
	set := newDepTestSet()
	mk := func(n string) Service {
		s := NewInternalService(set, n)
		set.AddService(s)
		return s
	}
	b, a, c := mk("b"), mk("a"), mk("c")
	for _, d := range [][2]Service{{a, b}, {c, b}, {a, c}} {
		if err := set.AddDependency(d[0], d[1], DepRegular); err != nil {
			t.Fatal(err)
		}
	}
	// A chain of 10 under b: b=10, c=11, a=12.
	prev := mk("d0")
	for i := 1; i < 10; i++ {
		s := mk(fmt.Sprintf("d%d", i))
		if err := set.AddDependency(s, prev, DepRegular); err != nil {
			t.Fatal(err)
		}
		prev = s
	}
	if err := set.AddDependency(b, prev, DepRegular); err != nil {
		t.Fatal(err)
	}
	if got := a.Record().DepDepth(); got != 12 {
		t.Errorf("a depth %d, want 12 (b=%d c=%d)", got, b.Record().DepDepth(), c.Record().DepDepth())
	}

	// Lengthening b's chain so a would sit at 33 must be refused.
	for i := 10; i < 31; i++ {
		s := mk(fmt.Sprintf("d%d", i))
		if err := set.AddDependency(s, prev, DepRegular); err != nil {
			t.Fatal(err)
		}
		prev = s
	}
	if err := set.AddDependency(b, prev, DepRegular); err == nil {
		t.Errorf("dependency accepted with a at depth %d, past MaxDepDepth=%d",
			a.Record().DepDepth(), MaxDepDepth)
	}
}

func TestCheckCircularDep(t *testing.T) {
	set := newDepTestSet()
	a := NewInternalService(set, "a")
	b := NewInternalService(set, "b")
	c := NewInternalService(set, "c")

	// a → b → c
	a.Record().AddDep(b, DepRegular)
	b.Record().AddDep(c, DepRegular)

	// Adding c → a would create a cycle
	if !CheckCircularDep(c, a) {
		t.Error("expected cycle c→a detected")
	}

	// Adding a → c would not create a cycle (already exists transitively, but no back-edge)
	if CheckCircularDep(a, c) {
		t.Error("no cycle expected for a→c")
	}

	// Unrelated service
	d := NewInternalService(set, "d")
	if CheckCircularDep(d, a) {
		t.Error("no cycle expected for d→a")
	}
}
