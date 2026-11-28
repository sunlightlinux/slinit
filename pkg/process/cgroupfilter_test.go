package process

import "testing"

// Taking away a filter a reload removed is best effort: with no cgroup
// to open (or bpf(2) refused) the start goes on, as a filter left there
// is only stricter.
func TestFilterDetachIsBestEffort(t *testing.T) {
	p := ExecParams{CgroupPath: "/nonexistent/cg", DeviceFilterOwnCgroup: true, DetachDeviceFilter: true, DetachIPFilter: true}
	if err := applyDeviceFilter(p, nil); err != nil {
		t.Errorf("device detach: %v", err)
	}
	if err := applyIPFilter(p, nil); err != nil {
		t.Errorf("ip detach: %v", err)
	}
	// A filter to attach with no cgroup still fails the start.
	p.DevicePolicy = "strict"
	if err := applyDeviceFilter(p, nil); err == nil {
		t.Error("a strict filter with no cgroup did not fail")
	}
	p.IPAddressDeny = []string{"any"}
	if err := applyIPFilter(p, nil); err == nil {
		t.Error("an ip filter with no cgroup did not fail")
	}
}
