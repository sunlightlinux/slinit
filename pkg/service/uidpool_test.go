package service

import "testing"

func TestUIDPoolDefaultsToSystemdRange(t *testing.T) {
	p := NewUIDPool(0, 0)
	min, max := p.Range()
	if min != 61184 || max != 65519 {
		t.Errorf("default range: got [%d,%d] want [61184,65519]", min, max)
	}
}

func TestUIDPoolAllocateSequential(t *testing.T) {
	p := NewUIDPool(100, 102)
	a, err := p.Allocate("svc-a")
	if err != nil {
		t.Fatalf("alloc a: %v", err)
	}
	if a != 100 {
		t.Errorf("a: got %d want 100", a)
	}
	b, err := p.Allocate("svc-b")
	if err != nil {
		t.Fatalf("alloc b: %v", err)
	}
	if b != 101 {
		t.Errorf("b: got %d want 101", b)
	}
	c, err := p.Allocate("svc-c")
	if err != nil {
		t.Fatalf("alloc c: %v", err)
	}
	if c != 102 {
		t.Errorf("c: got %d want 102", c)
	}
}

func TestUIDPoolExhaustion(t *testing.T) {
	p := NewUIDPool(100, 101)
	if _, err := p.Allocate("svc-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Allocate("svc-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Allocate("svc-c"); err == nil {
		t.Error("expected exhausted error")
	}
}

func TestUIDPoolReleaseAndReuse(t *testing.T) {
	p := NewUIDPool(100, 101)
	a, _ := p.Allocate("svc-a")
	b, _ := p.Allocate("svc-b")
	if _, err := p.Allocate("svc-c"); err == nil {
		t.Fatal("expected exhausted")
	}
	p.Release(a)
	c, err := p.Allocate("svc-c")
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	if c != a {
		t.Errorf("expected reuse of released UID %d, got %d", a, c)
	}
	_ = b
}

func TestUIDPoolReleaseUnknownIsNoOp(t *testing.T) {
	p := NewUIDPool(100, 101)
	p.Release(999) // not allocated; must not panic
	if p.InUse(999) {
		t.Error("released UID still tracked")
	}
}

func TestUIDPoolOwnerTracking(t *testing.T) {
	p := NewUIDPool(100, 101)
	a, _ := p.Allocate("my-svc")
	if owner := p.Owner(a); owner != "my-svc" {
		t.Errorf("owner: got %q want %q", owner, "my-svc")
	}
	if owner := p.Owner(999); owner != "" {
		t.Errorf("owner of free UID: got %q want empty", owner)
	}
}

func TestUIDPoolSwapsMinMax(t *testing.T) {
	// NewUIDPool(max, min) should produce the same range as
	// NewUIDPool(min, max) — swap on construction.
	p := NewUIDPool(102, 100)
	min, max := p.Range()
	if min != 100 || max != 102 {
		t.Errorf("swap: got [%d,%d] want [100,102]", min, max)
	}
}

// --- Integration: dynamic-user allocation through ServiceRecord ---

func TestDynamicUserAllocatesAndReleases(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "dyn")
	set.AddService(svc)

	rec := svc.Record()
	rec.SetDynamicUser(true)

	if err := rec.allocateDynamicUID(); err != nil {
		t.Fatalf("allocate: %v", err)
	}
	uid := rec.DynamicUID()
	if uid == 0 {
		t.Fatal("expected non-zero UID")
	}
	if !set.UIDPool().InUse(uid) {
		t.Error("pool should mark UID as in use")
	}

	// Re-allocate is a no-op when already held.
	if err := rec.allocateDynamicUID(); err != nil {
		t.Fatal(err)
	}
	if rec.DynamicUID() != uid {
		t.Errorf("UID changed on re-allocate: %d -> %d", uid, rec.DynamicUID())
	}

	rec.releaseDynamicUID()
	if rec.DynamicUID() != 0 {
		t.Error("UID should reset to 0 after release")
	}
	if set.UIDPool().InUse(uid) {
		t.Error("pool should release UID")
	}
}

func TestDynamicUserDisabledIsNoOp(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "static")
	set.AddService(svc)

	if err := svc.Record().allocateDynamicUID(); err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if svc.Record().DynamicUID() != 0 {
		t.Error("dynamic-user=false should not allocate")
	}
}
