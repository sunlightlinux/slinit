package service

import (
	"crypto/rand"
	"encoding/hex"
)

// newInvocationID mints a 128-bit random hex string used as the
// SLINIT_INVOCATION_ID emitted with every journal event during a
// service's start-to-stop cycle. Matches systemd's INVOCATION_ID
// UUID format (32 lowercase hex chars, no dashes) so operator tools
// and any cross-parity script that already understands systemd IDs
// keep working.
//
// crypto/rand failure is a boot-critical impossibility we treat as
// unrecoverable in practice — if the kernel can't seed randomness
// the whole init has bigger problems. Return "" so the caller emits
// no field rather than a garbled one; the journal event still fires.
func newInvocationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}
