package service

import (
	"fmt"
	"sync"
)

// Default dynamic-user pool: systemd uses 61184..65519 (0xEF00..0xFFEF)
// for transient users. We use the same range so admins inspecting
// /proc/<pid>/status see a familiar value.
const (
	dynamicUIDMin uint32 = 61184
	dynamicUIDMax uint32 = 65519
)

// UIDPool is an in-memory allocator for dynamic-user UIDs. It is
// per-ServiceSet — the pool does not survive a slinit restart, mirroring
// systemd's design (dynamic users are an isolation feature, not a
// stable identity).
//
// Allocation is deterministic for testing: the smallest free UID is
// always returned first. Releasing a UID makes it available again.
type UIDPool struct {
	mu     sync.Mutex
	min    uint32
	max    uint32
	inUse  map[uint32]string // uid → service name (for diagnostics)
}

// NewUIDPool creates an allocator covering [min, max] inclusive.
// If min/max are zero the systemd-style default range is used.
func NewUIDPool(min, max uint32) *UIDPool {
	if min == 0 && max == 0 {
		min = dynamicUIDMin
		max = dynamicUIDMax
	}
	if max < min {
		min, max = max, min
	}
	return &UIDPool{min: min, max: max, inUse: map[uint32]string{}}
}

// Allocate returns the smallest free UID in the pool, recording it as
// in use by `owner`. Returns an error when the pool is exhausted.
func (p *UIDPool) Allocate(owner string) (uint32, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for uid := p.min; uid <= p.max; uid++ {
		if _, taken := p.inUse[uid]; !taken {
			p.inUse[uid] = owner
			return uid, nil
		}
	}
	return 0, fmt.Errorf("dynamic-user pool exhausted (%d..%d all in use)", p.min, p.max)
}

// Release returns the UID to the pool. Safe to call on a UID that was
// never allocated (no-op).
func (p *UIDPool) Release(uid uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.inUse, uid)
}

// InUse reports whether the given UID is currently allocated.
func (p *UIDPool) InUse(uid uint32) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, taken := p.inUse[uid]
	return taken
}

// Owner returns the service name that holds the given UID, or "" if
// the UID is free.
func (p *UIDPool) Owner(uid uint32) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inUse[uid]
}

// Range returns the inclusive [min, max] UID range covered by the pool.
func (p *UIDPool) Range() (uint32, uint32) { return p.min, p.max }
