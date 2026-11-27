package service

import "testing"

// AddDependency(a, x, DepBefore) is "a starts before x", given as an
// operator writes it (slinitctl add-dep a before x) and stored the dinit
// way, on x. It was stored on a, and the holder of an ordering edge waits
// for its target, so a waited for x: the opposite. The services here
// start asynchronously, which the old TestBeforeOrdering (internal
// services, started in one call) could not see.
func TestBeforeEdgeOrdersTheDeclaringServiceFirst(t *testing.T) {
	set, _ := newTestSet()
	svcs := addTestSvcs(t, set, "a", "x", "boot")
	a, x, boot := svcs[0], svcs[1], svcs[2]
	for _, s := range svcs {
		s.autoStart = false
		s.autoStop = false
	}
	if err := set.AddDependency(a, x, DepBefore); err != nil {
		t.Fatal(err)
	}
	boot.Record().AddDep(x, DepRegular)
	boot.Record().AddDep(a, DepRegular)

	set.StartService(boot)
	set.ProcessQueues()
	if !a.upReqd {
		t.Fatal("a must be asked up: nothing orders it after anything")
	}
	if x.upReqd {
		t.Fatal("x was asked up while a, which starts before it, is still STARTING")
	}
	a.Started()
	set.ProcessQueues()
	if !x.upReqd {
		t.Fatal("x must be asked up once a has started")
	}

	// rm-dep takes the declared form too.
	if !set.RemoveDependency(a, x, DepBefore) {
		t.Fatal("RemoveDependency(a, x, DepBefore) found no edge")
	}
	for _, d := range x.Record().Dependencies() {
		if d.DepType == DepBefore {
			t.Errorf("the before edge is still on x after RemoveDependency")
		}
	}
}
