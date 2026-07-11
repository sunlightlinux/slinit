package service

import (
	"sort"
	"testing"
)

// profileTestLogger is a no-op logger for tests that don't need to
// assert on ServiceLogger interactions but must satisfy the type.
type profileTestLogger struct{}

func (profileTestLogger) ServiceStarted(string)          {}
func (profileTestLogger) ServiceStopped(string)          {}
func (profileTestLogger) ServiceFailed(string, bool)     {}
func (profileTestLogger) Error(string, ...interface{})   {}
func (profileTestLogger) Info(string, ...interface{})    {}

// TestProfileInProfileGlobalService confirms that a service without
// any profile tags is always considered "in" every profile — the
// property that makes global infrastructure (network, logger, dbus)
// safe under profile swaps.
func TestProfileInProfileGlobalService(t *testing.T) {
	rec := NewServiceRecord(nil, nil, "sshd", TypeProcess)
	if !rec.InProfile("prod") {
		t.Error("global service should be in every profile, got !InProfile('prod')")
	}
	if !rec.InProfile("") {
		t.Error("global service should be in the empty profile too")
	}
}

// TestProfileInProfileTagged confirms membership semantics for a
// service tagged with two profiles: yes to both, no to a third.
func TestProfileInProfileTagged(t *testing.T) {
	rec := NewServiceRecord(nil, nil, "app", TypeProcess)
	rec.SetProfiles([]string{"prod", "rescue"})
	if !rec.InProfile("prod") {
		t.Error("tagged 'prod' service should report InProfile('prod')")
	}
	if !rec.InProfile("rescue") {
		t.Error("tagged 'rescue' service should report InProfile('rescue')")
	}
	if rec.InProfile("dev") {
		t.Error("service NOT tagged 'dev' should not report InProfile('dev')")
	}
}

// TestServiceSetProfileAllows exercises the boot-time filter used by
// LoadService / boot loop: with no active profile every service is
// allowed; with an active profile, only global services and
// services carrying the tag pass.
func TestServiceSetProfileAllows(t *testing.T) {
	ss := NewServiceSet(profileTestLogger{})

	// Unset filter → everything allowed.
	if !ss.ProfileAllows(nil) {
		t.Error("unset filter: global service must be allowed")
	}
	if !ss.ProfileAllows([]string{"prod"}) {
		t.Error("unset filter: tagged service must be allowed")
	}

	ss.SetActiveProfile("prod")
	if !ss.ProfileAllows(nil) {
		t.Error("active=prod: global service (no tags) must still be allowed")
	}
	if !ss.ProfileAllows([]string{"prod"}) {
		t.Error("active=prod: prod-tagged service must be allowed")
	}
	if ss.ProfileAllows([]string{"dev"}) {
		t.Error("active=prod: dev-tagged service must NOT be allowed")
	}
}

// TestServiceSetListProfiles enumerates the sorted distinct tags
// across loaded services, skipping globals.
func TestServiceSetListProfiles(t *testing.T) {
	ss := NewServiceSet(profileTestLogger{})

	// Manually inject records so we don't depend on the full loader
	// path — that's covered by higher-level integration tests.
	svcA := NewProcessService(ss, "web")
	svcA.Record().SetProfiles([]string{"prod", "staging"})
	ss.mu.Lock()
	ss.records[svcA.Name()] = svcA
	ss.mu.Unlock()

	svcB := NewProcessService(ss, "batch")
	svcB.Record().SetProfiles([]string{"prod"})
	ss.mu.Lock()
	ss.records[svcB.Name()] = svcB
	ss.mu.Unlock()

	svcC := NewProcessService(ss, "sshd")
	// no profile tags → global
	ss.mu.Lock()
	ss.records[svcC.Name()] = svcC
	ss.mu.Unlock()

	got := ss.ListProfiles()
	want := []string{"prod", "staging"}
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("ListProfiles = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("ListProfiles[%d] = %q, want %q", i, got[i], w)
		}
	}
}

// TestActivateProfileRejectsUnknown confirms the safety-net: a typo'd
// profile name must not be allowed to silently stop every
// profile-tagged service. Real-world story: an operator types
// "activate-profile prd" (missing 'o'). Without validation, every
// service tagged 'prod' would immediately stop.
func TestActivateProfileRejectsUnknown(t *testing.T) {
	ss := NewServiceSet(profileTestLogger{})

	svc := NewProcessService(ss, "web")
	svc.Record().SetProfiles([]string{"prod"})
	ss.mu.Lock()
	ss.records[svc.Name()] = svc
	ss.mu.Unlock()

	if _, err := ss.ActivateProfile("nonexistent"); err == nil {
		t.Fatal("ActivateProfile with unknown name should error, got nil")
	}
	if ss.ActiveProfile() != "" {
		t.Errorf("failed activation must not mutate active profile; got %q",
			ss.ActiveProfile())
	}
}

// TestActivateProfileCategorizes drives the delta computation:
// service in old-only → stop; service in new-only → start;
// service in both → keep; global service → keep untouched.
func TestActivateProfileCategorizes(t *testing.T) {
	ss := NewServiceSet(profileTestLogger{})

	inject := func(name string, profiles []string) Service {
		svc := NewInternalService(ss, name) // Internal type has BringUp/Down no-ops safe for tests
		svc.Record().SetProfiles(profiles)
		ss.mu.Lock()
		ss.records[svc.Name()] = svc
		ss.mu.Unlock()
		return svc
	}
	inject("web", []string{"prod"})       // prod-only → new
	inject("batch", []string{"dev"})      // dev-only → old
	inject("common", []string{"prod", "dev"}) // both → keep
	inject("sshd", nil)                    // global → keep untouched

	// Start with dev active.
	ss.SetActiveProfile("dev")

	res, err := ss.ActivateProfile("prod")
	if err != nil {
		t.Fatalf("ActivateProfile: %v", err)
	}
	if res.Previous != "dev" || res.Active != "prod" {
		t.Errorf("bad transition report: %+v", res)
	}
	if len(res.Stopped) != 1 || res.Stopped[0] != "batch" {
		t.Errorf("Stopped = %v, want [batch]", res.Stopped)
	}
	if len(res.Started) != 1 || res.Started[0] != "web" {
		t.Errorf("Started = %v, want [web]", res.Started)
	}
	// Kept must include 'common' (tagged for both) but must NOT
	// include 'sshd' (global — reported as untouched infrastructure,
	// deliberately excluded from the noisy "kept" list).
	if len(res.Kept) != 1 || res.Kept[0] != "common" {
		t.Errorf("Kept = %v, want [common]", res.Kept)
	}
}

// TestActivateProfileNoOp confirms same-profile activation is a
// silent no-op — no delta, no service touched.
func TestActivateProfileNoOp(t *testing.T) {
	ss := NewServiceSet(profileTestLogger{})

	svc := NewInternalService(ss, "web")
	svc.Record().SetProfiles([]string{"prod"})
	ss.mu.Lock()
	ss.records[svc.Name()] = svc
	ss.mu.Unlock()

	ss.SetActiveProfile("prod")
	res, err := ss.ActivateProfile("prod")
	if err != nil {
		t.Fatalf("no-op ActivateProfile errored: %v", err)
	}
	if len(res.Stopped) != 0 || len(res.Started) != 0 {
		t.Errorf("no-op activation must not move any service; got %+v", res)
	}
}
