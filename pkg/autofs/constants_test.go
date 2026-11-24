package autofs

import (
	"testing"
	"unsafe"
)

// ioc is the kernel's generic _IOC() (include/uapi/asm-generic/ioctl.h).
func ioc(dir, nr, size uintptr) uintptr {
	return dir<<30 | size<<16 | iocMagic<<8 | nr
}

const (
	iocNone  = 0
	iocWrite = 1
	iocRead  = 2
)

// TestIoctlNumbers checks each request against the definition in
// include/uapi/linux/auto_fs.h. A wrong number makes the kernel fail
// the call with ENOTTY/EINVAL, so the feature silently never works.
func TestIoctlNumbers(t *testing.T) {
	const (
		cInt   = 4                         // int
		cULong = unsafe.Sizeof(uintptr(0)) // unsigned long
		expire = 4 + 4 + 4 + 256           // struct autofs_packet_expire
	)
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"READY", AUTOFS_IOC_READY, ioc(iocNone, 0x60, 0)},
		{"FAIL", AUTOFS_IOC_FAIL, ioc(iocNone, 0x61, 0)},
		{"CATATONIC", AUTOFS_IOC_CATATONIC, ioc(iocNone, 0x62, 0)},
		{"PROTOVER", AUTOFS_IOC_PROTOVER, ioc(iocRead, 0x63, cInt)},
		{"SETTIMEOUT", AUTOFS_IOC_SETTIMEOUT, ioc(iocRead|iocWrite, 0x64, cULong)},
		{"EXPIRE", AUTOFS_IOC_EXPIRE, ioc(iocRead, 0x65, expire)},
		{"EXPIRE_MULTI", AUTOFS_IOC_EXPIRE_MULTI, ioc(iocWrite, 0x66, cInt)},
	} {
		if c.got != c.want {
			t.Errorf("AUTOFS_IOC_%s = %#x, kernel's is %#x", c.name, c.got, c.want)
		}
	}
}
