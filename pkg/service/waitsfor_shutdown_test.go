package service

import "testing"

// boot waits-for a and z, and a waits-for z, with a listed first. Found on
// a GNOME install: boot.d held gdm and polkitd, and gdm waits-for polkitd;
// at shutdown polkitd — and logind, dbus and early-setup beneath it —
// never got a stop, and slinit sat out its 90-second emergency timeout.
func TestStopAllServicesSharedWaitsFor(t *testing.T) {
	set, _ := newTestSet()
	boot := NewInternalService(set, "boot")
	a := NewInternalService(set, "a")
	z := NewInternalService(set, "z")
	for _, s := range []Service{boot, a, z} {
		set.AddService(s)
	}
	boot.Record().AddDep(a, DepWaitsFor)
	boot.Record().AddDep(z, DepWaitsFor)
	a.Record().AddDep(z, DepWaitsFor)

	set.StartService(boot)
	for _, s := range []Service{boot, a, z} {
		if s.State() != StateStarted {
			t.Fatalf("%s not started: %v", s.Name(), s.State())
		}
	}
	// z has two dependents, boot and a; a third count is the leak.
	if got := z.Record().requiredBy; got != 2 {
		t.Errorf("z requiredBy = %d with two dependents, want 2", got)
	}

	set.StopAllServices(ShutdownHalt)
	for _, s := range []Service{boot, a, z} {
		if s.State() != StateStopped {
			t.Errorf("%s still %v after StopAllServices (requiredBy=%d)", s.Name(), s.State(), s.Record().requiredBy)
		}
	}
}
