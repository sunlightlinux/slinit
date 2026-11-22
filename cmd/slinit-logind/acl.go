// POSIX access ACLs, encoded the way the kernel stores them in the
// "system.posix_acl_access" extended attribute — what libacl's
// acl_set_file writes. slinit-logind needs only one operation on them:
// give one user read/write on a device node and take it from everyone
// else it was given to (uaccess.go), so it does that without libacl.
//
// Layout: a 4-byte little-endian version (2), then 8-byte entries of
// tag (u16), permissions (u16) and qualifier id (u32), sorted by tag and,
// for named users and groups, by id.
package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
)

const (
	aclXattr   = "system.posix_acl_access"
	aclVersion = 2

	aclUserObj  = 0x01
	aclUser     = 0x02
	aclGroupObj = 0x04
	aclGroup    = 0x08
	aclMask     = 0x10
	aclOther    = 0x20

	aclUndefinedID = 0xFFFFFFFF

	aclRead  = 4
	aclWrite = 2
)

type aclEntry struct {
	tag  uint16
	perm uint16
	id   uint32
}

func decodeACL(b []byte) ([]aclEntry, error) {
	if len(b) < 4 || (len(b)-4)%8 != 0 {
		return nil, fmt.Errorf("acl: %d bytes is not a POSIX ACL", len(b))
	}
	if v := binary.LittleEndian.Uint32(b[:4]); v != aclVersion {
		return nil, fmt.Errorf("acl: version %d, want %d", v, aclVersion)
	}
	var out []aclEntry
	for i := 4; i < len(b); i += 8 {
		out = append(out, aclEntry{
			tag:  binary.LittleEndian.Uint16(b[i:]),
			perm: binary.LittleEndian.Uint16(b[i+2:]),
			id:   binary.LittleEndian.Uint32(b[i+4:]),
		})
	}
	return out, nil
}

func encodeACL(entries []aclEntry) []byte {
	b := make([]byte, 4+8*len(entries))
	binary.LittleEndian.PutUint32(b, aclVersion)
	for i, e := range entries {
		o := 4 + 8*i
		binary.LittleEndian.PutUint16(b[o:], e.tag)
		binary.LittleEndian.PutUint16(b[o+2:], e.perm)
		binary.LittleEndian.PutUint32(b[o+4:], e.id)
	}
	return b
}

// aclFromMode is the minimal ACL equivalent to a file's permission bits,
// for a node that has no ACL attribute yet.
func aclFromMode(mode uint32) []aclEntry {
	return []aclEntry{
		{aclUserObj, uint16(mode>>6) & 7, aclUndefinedID},
		{aclGroupObj, uint16(mode>>3) & 7, aclUndefinedID},
		{aclOther, uint16(mode) & 7, aclUndefinedID},
	}
}

// grantOnlyUser returns acl with every named-user entry removed and, when
// uid is non-negative, one read/write entry for uid added; the mask is
// recomputed from the group class, and dropped when no named entry is
// left, so the kernel folds a now-minimal ACL back into the mode bits.
func grantOnlyUser(acl []aclEntry, uid int64) ([]aclEntry, error) {
	var out []aclEntry
	var hasUserObj, hasGroupObj, hasOther bool
	for _, e := range acl {
		switch e.tag {
		case aclUser, aclMask:
			continue
		case aclUserObj:
			hasUserObj = true
		case aclGroupObj:
			hasGroupObj = true
		case aclOther:
			hasOther = true
		}
		out = append(out, e)
	}
	if !hasUserObj || !hasGroupObj || !hasOther {
		return nil, errors.New("acl: missing owner, group or other entry")
	}
	if uid >= 0 {
		out = append(out, aclEntry{aclUser, aclRead | aclWrite, uint32(uid)})
	}
	named := false
	var mask uint16
	for _, e := range out {
		switch e.tag {
		case aclUser, aclGroup:
			named = true
			mask |= e.perm
		case aclGroupObj:
			mask |= e.perm
		}
	}
	if named {
		out = append(out, aclEntry{aclMask, mask, aclUndefinedID})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].tag != out[j].tag {
			return out[i].tag < out[j].tag
		}
		return out[i].id < out[j].id
	})
	return out, nil
}
