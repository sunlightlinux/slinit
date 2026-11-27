//go:build !((386 || amd64) && gc)

package service

// virtCPUID is nil where there is no CPUID: systemd's detect_vm_cpuid
// is x86-only too. Tests may set it to exercise the x86 path.
var virtCPUID func() (bool, string)
