package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckMountsFor(t *testing.T) {
	dir := t.TempDir()
	tab := filepath.Join(dir, "fstab")
	os.WriteFile(tab, []byte("UUID=a / ext4 defaults 0 1\nUUID=b /var ext4 defaults 0 2\n"+
		"UUID=c /var/lib/data xfs noauto 0 0\nUUID=d none swap sw 0 0\nUUID=e /home ext4 defaults 0 2\n"+
		"UUID=f /variable ext4 defaults 0 2\n"), 0o644)
	mounted := map[string]bool{"/var": true}
	isMount := func(p string) (bool, string) { return mounted[p], "" }
	for _, c := range []struct {
		path string
		ok   bool
		why  string
	}{
		{"/var/lib/foo", true, ""},                  // /var mounted; /var/lib/data is not on the way
		{"/var/lib/data/x", false, "/var/lib/data"}, // noauto, not mounted
		{"/home/u", false, "/home"},
		{"/var", true, ""},
		{"/usr/share", true, ""}, // only / on the way
		{"/var/../home/x", false, "/home"},
		{"relative", false, "absolute"},
	} {
		ok, why := checkMountsFor(c.path, tab, isMount)
		if ok != c.ok || !strings.Contains(why, c.why) {
			t.Errorf("%s: %v %q, want %v (%q)", c.path, ok, why, c.ok, c.why)
		}
	}
	// /variable is not on the way to /var/x: a prefix of the name only.
	mounted = map[string]bool{"/var": true}
	if ok, why := checkMountsFor("/var/x", tab, isMount); !ok {
		t.Errorf("/variable taken for a parent of /var/x: %s", why)
	}
	if ok, _ := checkMountsFor("/var/x", filepath.Join(dir, "nofstab"), isMount); !ok {
		t.Error("no fstab: does not hold")
	}
	// Through the predicate, as assert-mounts-for.
	k, ok := PredicateKindByName("mounts-for")
	if s := (Predicate{Kind: k, IsAssert: true, Param: "/x"}).String(); !ok || k != PredMountsFor || !strings.Contains(s, "assert-mounts-for") {
		t.Errorf("kind %v %v %q", k, ok, s)
	}
}
