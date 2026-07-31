package journald

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

// castUcred reinterprets the first SizeofUcred bytes of b as a
// *unix.Ucred and returns it. The kernel writes SCM_CREDENTIALS in
// the exact struct layout expected by unix.Ucred, so no field-by-field
// copy is needed.
//
// The caller MUST ensure len(b) >= unix.SizeofUcred (checked before
// reaching this helper). Placed in its own file so the single
// unsafe.Pointer use in this package is trivially auditable.
func castUcred(b []byte) *unix.Ucred {
	return (*unix.Ucred)(unsafe.Pointer(&b[0]))
}
