package config

import (
	"os/user"
	"strconv"
	"testing"
)

// Regression: desc.RunAs was parsed but never plumbed to the service
// (loader.go only consumed it for ExportPasswdVars). resolveRunAs
// codifies the "<user>[:<group>]" grammar so a future refactor can't
// silently drop it again.
func TestResolveRunAsRoot(t *testing.T) {
	// Skip if the host doesn't have a usable root entry — covers
	// container-builder edges where /etc/passwd may be stub.
	u, err := user.Lookup("root")
	if err != nil {
		t.Skipf("root not resolvable on this host: %v", err)
	}

	uid, gid, ok := resolveRunAs("root")
	if !ok {
		t.Fatal("resolveRunAs(root): ok = false")
	}
	wantUID, _ := strconv.ParseUint(u.Uid, 10, 32)
	wantGID, _ := strconv.ParseUint(u.Gid, 10, 32)
	if uint64(uid) != wantUID {
		t.Errorf("uid = %d, want %d", uid, wantUID)
	}
	if uint64(gid) != wantGID {
		t.Errorf("gid = %d, want %d", gid, wantGID)
	}
}

func TestResolveRunAsNumeric(t *testing.T) {
	// Numeric uid must work even if no /etc/passwd entry exists for it.
	uid, gid, ok := resolveRunAs("0")
	if !ok {
		t.Fatal("resolveRunAs(\"0\"): ok = false")
	}
	if uid != 0 || gid != 0 {
		t.Errorf("uid=%d gid=%d, want 0/0", uid, gid)
	}
}

func TestResolveRunAsUnknown(t *testing.T) {
	_, _, ok := resolveRunAs("nosuchuser-acceptance-probe")
	if ok {
		t.Fatal("resolveRunAs of unknown user: ok = true (expected false)")
	}
}

func TestResolveRunAsEmpty(t *testing.T) {
	_, _, ok := resolveRunAs("")
	if ok {
		t.Fatal("resolveRunAs(\"\"): ok = true (expected false)")
	}
}

func TestResolveRunAsUserGroupSplit(t *testing.T) {
	// `root:root` should parse as user=root, group=root and resolve to
	// (0, 0). The colon split is what allows configurations like
	// `run-as = www-data:www-data` to land both ids.
	if _, err := user.Lookup("root"); err != nil {
		t.Skipf("root not resolvable: %v", err)
	}
	uid, gid, ok := resolveRunAs("root:root")
	if !ok {
		t.Fatal("resolveRunAs(\"root:root\"): ok = false")
	}
	if uid != 0 || gid != 0 {
		t.Errorf("uid=%d gid=%d, want 0/0", uid, gid)
	}
}
