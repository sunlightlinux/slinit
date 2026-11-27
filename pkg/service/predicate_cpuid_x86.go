//go:build (386 || amd64) && gc

package service

import (
	"encoding/binary"
	"strings"
)

// cpuid executes the CPUID instruction (predicate_cpuid_x86.s).
func cpuid(eaxArg, ecxArg uint32) (eax, ebx, ecx, edx uint32)

// hostCPUIDHypervisor is systemd's detect_vm_cpuid: CPUID leaf 1 ECX
// bit 31 says a hypervisor is present, and leaf 0x40000000 carries its
// 12-byte vendor signature in EBX, ECX, EDX. The signature is how
// systemd tells KVM ("KVMKVMKVM") from QEMU without KVM ("TCGTCGTCGTCG"),
// which DMI cannot: both present "QEMU" there. /proc/cpuinfo shows the
// hypervisor flag but not the signature.
func hostCPUIDHypervisor() (bool, string) {
	if max, _, _, _ := cpuid(0, 0); max < 1 {
		return false, ""
	}
	_, _, ecx, _ := cpuid(1, 0)
	if ecx&(1<<31) == 0 {
		return false, ""
	}
	_, ebx, ecx, edx := cpuid(0x40000000, 0)
	var sig [12]byte
	binary.LittleEndian.PutUint32(sig[0:], ebx)
	binary.LittleEndian.PutUint32(sig[4:], ecx)
	binary.LittleEndian.PutUint32(sig[8:], edx)
	return true, strings.TrimRight(string(sig[:]), "\x00")
}

// virtCPUID is the CPUID probe detectVM uses; tests replace it.
var virtCPUID func() (bool, string) = hostCPUIDHypervisor
