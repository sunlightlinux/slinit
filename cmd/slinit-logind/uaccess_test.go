package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
)

func TestACLRoundTrip(t *testing.T) {
	in := []aclEntry{{aclUserObj, 6, aclUndefinedID}, {aclUser, 6, 1000}, {aclGroupObj, 6, aclUndefinedID}, {aclMask, 6, aclUndefinedID}, {aclOther, 0, aclUndefinedID}}
	out, err := decodeACL(encodeACL(in))
	if err != nil || !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip: %v %v", out, err)
	}
	for _, bad := range [][]byte{nil, {2, 0, 0}, {3, 0, 0, 0}, {2, 0, 0, 0, 1}} {
		if _, err := decodeACL(bad); err == nil {
			t.Errorf("decodeACL(%v) accepted", bad)
		}
	}
}

// /dev/dri/card0 is root:video 0660. The user at the seat gets rw through
// a named entry and a mask; the previous one loses theirs; with nobody
// at the seat the ACL is back to the mode bits.
func TestGrantOnlyUser(t *testing.T) {
	base := aclFromMode(0o660)
	got, err := grantOnlyUser(base, 1000)
	if err != nil {
		t.Fatal(err)
	}
	want := []aclEntry{{aclUserObj, 6, aclUndefinedID}, {aclUser, 6, 1000}, {aclGroupObj, 6, aclUndefinedID}, {aclMask, 6, aclUndefinedID}, {aclOther, 0, aclUndefinedID}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("grant 1000:\n got %v\nwant %v", got, want)
	}
	moved, _ := grantOnlyUser(got, 1001)
	var users []uint32
	for _, e := range moved {
		if e.tag == aclUser {
			users = append(users, e.id)
		}
	}
	if !reflect.DeepEqual(users, []uint32{1001}) {
		t.Errorf("after moving to 1001 the named users are %v", users)
	}
	none, _ := grantOnlyUser(moved, -1)
	if !reflect.DeepEqual(none, base) {
		t.Errorf("nobody at the seat:\n got %v\nwant %v", none, base)
	}
	// A named group another tool set stays, and keeps the mask.
	withGroup := append(aclFromMode(0o660), aclEntry{aclGroup, 4, 44})
	kept, _ := grantOnlyUser(withGroup, -1)
	if len(kept) != 5 || kept[2].tag != aclGroup || kept[3].tag != aclMask {
		t.Errorf("named group dropped or mask missing: %v", kept)
	}
	if _, err := grantOnlyUser([]aclEntry{{aclUserObj, 6, aclUndefinedID}}, 1000); err == nil {
		t.Error("an ACL without group and other entries was accepted")
	}
}

// Against the kernel: the ACL written must be the one read back, on a
// file of our own (tmpfs supports ACLs when the kernel has them).
func TestSetDeviceUserOnARealFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "card0")
	if err := os.WriteFile(p, nil, 0o660); err != nil {
		t.Fatal(err)
	}
	uid := int64(os.Getuid() + 12345)
	if err := setDeviceUser(p, uid); err != nil {
		if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) {
			t.Skip("no POSIX ACLs on the test filesystem")
		}
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	n, err := unix.Getxattr(p, aclXattr, buf)
	if err != nil {
		t.Fatal(err)
	}
	acl, _ := decodeACL(buf[:n])
	found := false
	for _, e := range acl {
		if e.tag == aclUser && int64(e.id) == uid && e.perm == aclRead|aclWrite {
			found = true
		}
	}
	if !found {
		t.Fatalf("kernel ACL after granting %d: %v", uid, acl)
	}
	if err := setDeviceUser(p, -1); err != nil {
		t.Fatal(err)
	}
	if _, err := unix.Getxattr(p, aclXattr, buf); !errors.Is(err, unix.ENODATA) {
		t.Errorf("after revoking, the ACL attribute is still there (%v)", err)
	}
}

// The seat's active user gets its devices, only that seat's, and only
// once per change; root needs no entry; nobody at the seat takes them back.
func TestApplySeatAccess(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "tags", "uaccess"), 0o755)
	os.MkdirAll(filepath.Join(dir, "data"), 0o755)
	for _, id := range []string{"c226:0", "c116:2", "b11:0", "c13:70", "+sound:card0"} {
		os.WriteFile(filepath.Join(dir, "tags", "uaccess", id), nil, 0o644)
	}
	os.WriteFile(filepath.Join(dir, "data", "c13:70"), []byte("E:ID_SEAT=seat1\nE:SUBSYSTEM=input\n"), 0o644)

	type call struct {
		path string
		uid  int64
	}
	var calls []call
	oldDir, oldPath, oldSet, oldOn := udevRunDir, devNodePathFunc, setDeviceUserFunc, uaccessEnabled
	udevRunDir, uaccessEnabled = dir, true
	devNodePathFunc = func(kind byte, maj, min string) (string, error) {
		return "/dev/" + string(kind) + maj + ":" + min, nil
	}
	setDeviceUserFunc = func(path string, uid int64) error { calls = append(calls, call{path, uid}); return nil }
	uaccessMu.Lock()
	uaccessUser = map[string]int64{}
	uaccessMu.Unlock()
	t.Cleanup(func() {
		udevRunDir, devNodePathFunc, setDeviceUserFunc, uaccessEnabled = oldDir, oldPath, oldSet, oldOn
		uaccessUser = map[string]int64{}
	})

	applySeatAccess("seat0", 1000, true)
	if len(calls) != 3 {
		t.Fatalf("seat0 devices for uid 1000: %v (want card, sound, cdrom — not seat1's input, not the nodeless sound card)", calls)
	}
	for _, c := range calls {
		if c.uid != 1000 {
			t.Errorf("%s granted to %d", c.path, c.uid)
		}
	}
	calls = nil
	applySeatAccess("seat0", 1000, true)
	if len(calls) != 0 {
		t.Errorf("same user again rewrote %d ACLs", len(calls))
	}
	applySeatAccess("seat0", 996, true)
	if len(calls) != 3 || calls[0].uid != 996 {
		t.Errorf("switch to the greeter user: %v", calls)
	}
	calls = nil
	applySeatAccess("seat0", 0, true)
	applySeatAccess("seat0", 0, false)
	if len(calls) != 3 || calls[0].uid != -1 {
		t.Errorf("root at the seat, then nobody: %v (want one revoke pass)", calls)
	}
	calls = nil
	applySeatAccess("seat1", 1001, true)
	if len(calls) != 1 || calls[0].path != "/dev/c13:70" {
		t.Errorf("seat1: %v", calls)
	}
	calls = nil
	uaccessEnabled = false
	applySeatAccess("seat0", 1002, true)
	if len(calls) != 0 {
		t.Errorf("disabled (user instance, tests) still wrote %v", calls)
	}
}
