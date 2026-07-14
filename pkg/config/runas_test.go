package config

import (
	"os/user"
	"strconv"
	"strings"
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

// TestResolveSupplementaryGroupsNumeric exercises the pure-numeric
// path so the helper is testable on hosts without a rich /etc/group.
// De-duplication and order preservation are load-bearing: the runner
// passes the list to setgroups(2) verbatim and the kernel does not
// itself de-dup.
func TestResolveSupplementaryGroupsNumeric(t *testing.T) {
	gids := resolveSupplementaryGroups("svc", []string{"0", "10", "0", "20"})
	if len(gids) != 3 {
		t.Fatalf("len = %d, want 3 (dedup collapses the second 0); got %v", len(gids), gids)
	}
	if gids[0] != 0 || gids[1] != 10 || gids[2] != 20 {
		t.Errorf("gids = %v, want [0 10 20] in that order", gids)
	}
}

// TestResolveSupplementaryGroupsEmpty covers the two flavours of
// "nothing to install": nil input and a slice of blanks. Both must
// return nil so the caller can treat "unset" and "explicitly blank"
// identically (see loader.applySupplementaryGroups no-op path).
func TestResolveSupplementaryGroupsEmpty(t *testing.T) {
	if got := resolveSupplementaryGroups("svc", nil); got != nil {
		t.Errorf("nil input: got %v, want nil", got)
	}
	if got := resolveSupplementaryGroups("svc", []string{"", "  "}); got != nil {
		t.Errorf("blank input: got %v, want nil", got)
	}
}

// TestResolveSupplementaryGroupsUnknownSkipped verifies the
// intentional forgiving behaviour: an unresolvable group name is
// logged and skipped, not fatal. Rationale mirrors resolveRunAs's
// forgiveness — a typo shouldn't drop the whole service.
func TestResolveSupplementaryGroupsUnknownSkipped(t *testing.T) {
	gids := resolveSupplementaryGroups("svc",
		[]string{"nosuchgroup-supp-probe", "0"})
	if len(gids) != 1 || gids[0] != 0 {
		t.Errorf("gids = %v, want [0] (unknown skipped)", gids)
	}
}

// TestParseSupplementaryGroupsScalar verifies the space-separated
// list parser + = vs += semantics. The whole point of a directive
// (over shoving supplementary groups into `run-as = user:g1:g2:g3`
// chpst-style) is that this scans as a list.
func TestParseSupplementaryGroupsScalar(t *testing.T) {
	src := `
type = process
command = /bin/true
supplementary-groups = wheel adm ssl-cert
`
	desc, err := Parse(strings.NewReader(src), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"wheel", "adm", "ssl-cert"}
	if got := desc.SupplementaryGroups; !stringSliceEq(got, want) {
		t.Errorf("SupplementaryGroups = %v, want %v", got, want)
	}
}

func TestParseSupplementaryGroupsAppend(t *testing.T) {
	src := `
type = process
command = /bin/true
supplementary-groups = wheel
supplementary-groups += adm ssl-cert
`
	desc, err := Parse(strings.NewReader(src), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"wheel", "adm", "ssl-cert"}
	if got := desc.SupplementaryGroups; !stringSliceEq(got, want) {
		t.Errorf("SupplementaryGroups = %v, want %v", got, want)
	}
}

// TestParseSupplementaryGroupsRebind covers the = (not +=) case
// after a prior assignment: the second line REPLACES rather than
// appends. Matches every other list directive in slinit.
func TestParseSupplementaryGroupsRebind(t *testing.T) {
	src := `
type = process
command = /bin/true
supplementary-groups = wheel adm
supplementary-groups = ssl-cert
`
	desc, err := Parse(strings.NewReader(src), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"ssl-cert"}
	if got := desc.SupplementaryGroups; !stringSliceEq(got, want) {
		t.Errorf("SupplementaryGroups = %v, want %v", got, want)
	}
}

func stringSliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
