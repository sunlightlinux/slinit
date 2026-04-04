package service

import "testing"

func TestGetActiveServiceInfo_Empty(t *testing.T) {
	set, _ := newTestSet()

	info := set.GetActiveServiceInfo()
	if len(info) != 0 {
		t.Errorf("expected empty active info, got %d entries", len(info))
	}
}

func TestGetActiveServiceInfo_MixedStates(t *testing.T) {
	set, _ := newTestSet()

	// Create and start some services
	svc1 := NewInternalService(set, "running-svc")
	set.AddService(svc1)
	set.StartService(svc1) // → STARTED

	svc2 := NewInternalService(set, "stopped-svc")
	set.AddService(svc2) // remains STOPPED

	svc3 := NewInternalService(set, "another-running")
	set.AddService(svc3)
	set.StartService(svc3) // → STARTED

	info := set.GetActiveServiceInfo()
	if len(info) != 2 {
		t.Fatalf("expected 2 active services, got %d", len(info))
	}

	names := map[string]bool{}
	for _, i := range info {
		names[i.Name] = true
		if i.State != StateStarted {
			t.Errorf("expected STARTED for %s, got %v", i.Name, i.State)
		}
	}
	if !names["running-svc"] || !names["another-running"] {
		t.Errorf("expected running-svc and another-running, got %v", names)
	}
}

func TestGetActiveServiceInfo_PID(t *testing.T) {
	set, _ := newTestSet()

	// Internal services have PID -1
	svc := NewInternalService(set, "internal")
	set.AddService(svc)
	set.StartService(svc)

	info := set.GetActiveServiceInfo()
	if len(info) != 1 {
		t.Fatalf("expected 1 active, got %d", len(info))
	}
	if info[0].PID != -1 {
		t.Errorf("expected PID -1 for internal service, got %d", info[0].PID)
	}
}

func TestKillActiveServices_NoPanic(t *testing.T) {
	set, _ := newTestSet()

	// Should not panic with no services
	set.KillActiveServices()

	// Should not panic with internal services (PID -1)
	svc := NewInternalService(set, "internal")
	set.AddService(svc)
	set.StartService(svc)
	set.KillActiveServices() // PID -1, kill should be skipped
}
