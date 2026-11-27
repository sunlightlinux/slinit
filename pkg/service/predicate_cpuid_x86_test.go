//go:build (386 || amd64) && gc

package service

import (
	"os"
	"strings"
	"testing"
)

// TestHostCPUIDHypervisor checks the CPUID trampoline against the
// kernel: the hypervisor bit is what /proc/cpuinfo calls "hypervisor",
// and a hypervisor has a printable signature.
func TestHostCPUIDHypervisor(t *testing.T) {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		t.Skipf("no /proc/cpuinfo: %v", err)
	}
	var flags []string
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "flags" {
			flags = strings.Fields(v)
			break
		}
	}
	if flags == nil {
		t.Skip("no flags line")
	}
	kernel := false
	for _, f := range flags {
		kernel = kernel || f == "hypervisor"
	}
	hv, sig := hostCPUIDHypervisor()
	if hv != kernel {
		t.Fatalf("CPUID hypervisor bit = %v, /proc/cpuinfo says %v", hv, kernel)
	}
	if hv {
		if sig == "" || strings.ContainsFunc(sig, func(r rune) bool { return r < ' ' || r > '~' }) {
			t.Fatalf("hypervisor signature %q is not printable", sig)
		}
		t.Logf("hypervisor signature %q, detected %q", sig, virtCPUIDSigs[sig])
	}
}
