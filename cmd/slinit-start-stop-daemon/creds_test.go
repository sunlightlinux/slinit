package main

import (
	"errors"
	"os"
	"os/user"
	"reflect"
	"strconv"
	"testing"
)

// A --chuid child must get its user's supplementary groups
// (initgroups), not the empty list Go's exec sets when Groups is nil.
func TestSpawnCredentialUserGetsSupplementaryGroups(t *testing.T) {
	var asked string
	groupsOf := func(spec string) ([]string, error) {
		asked = spec
		return []string{"100", "200", "100", "junk"}, nil
	}
	cred, err := spawnCredential(Options{ChUID: "54321:777"}, groupsOf)
	if err != nil {
		t.Fatal(err)
	}
	if asked != "54321" {
		t.Errorf("groups looked up for %q, want the user part", asked)
	}
	if cred.Uid != 54321 || cred.Gid != 777 {
		t.Errorf("uid/gid = %d/%d", cred.Uid, cred.Gid)
	}
	if cred.NoSetGroups {
		t.Error("NoSetGroups set: the caller's groups would leak to the child")
	}
	if want := []uint32{777, 100, 200}; !reflect.DeepEqual(cred.Groups, want) {
		t.Errorf("Groups = %v, want %v", cred.Groups, want)
	}

	// --user stands in for --chuid on --start.
	cred, err = spawnCredential(Options{MatchUser: "54321", Group: "777"}, groupsOf)
	if err != nil {
		t.Fatal(err)
	}
	if want := []uint32{777, 100, 200}; !reflect.DeepEqual(cred.Groups, want) {
		t.Errorf("--user: Groups = %v, want %v", cred.Groups, want)
	}
}

// A failed lookup (numeric uid with no passwd entry) gives the child
// its gid alone, never the caller's groups.
func TestSpawnCredentialLookupFailure(t *testing.T) {
	cred, err := spawnCredential(Options{ChUID: "54321:777"},
		func(string) ([]string, error) { return nil, errors.New("no such user") })
	if err != nil {
		t.Fatal(err)
	}
	if cred.NoSetGroups || !reflect.DeepEqual(cred.Groups, []uint32{777}) {
		t.Errorf("Groups = %v NoSetGroups=%v, want [777] false", cred.Groups, cred.NoSetGroups)
	}
}

// Only --group: as OpenRC and Debian, setgid and keep the inherited
// supplementary groups (no setgroups call).
func TestSpawnCredentialGroupOnly(t *testing.T) {
	called := false
	cred, err := spawnCredential(Options{Group: "777"},
		func(string) ([]string, error) { called = true; return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("group lookup without a user")
	}
	if cred.Gid != 777 || int(cred.Uid) != os.Getuid() {
		t.Errorf("uid/gid = %d/%d", cred.Uid, cred.Gid)
	}
	if !cred.NoSetGroups || cred.Groups != nil {
		t.Errorf("group-only: NoSetGroups=%v Groups=%v, want true nil", cred.NoSetGroups, cred.Groups)
	}
}

// --chuid with a numeric uid takes that user's passwd gid; a name-only
// lookup failed and left the caller's (root's) gid on the child.
func TestLookupPrimaryGIDNumeric(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	gid, err := lookupPrimaryGID(me.Uid)
	if err != nil {
		t.Fatalf("lookupPrimaryGID(%s): %v", me.Uid, err)
	}
	if strconv.Itoa(gid) != me.Gid {
		t.Errorf("gid = %d, want %s", gid, me.Gid)
	}
}

func TestUserGroupIDsIncludesPrimary(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	ids, err := userGroupIDs(me.Uid)
	if err != nil {
		t.Skip(err)
	}
	for _, id := range ids {
		if id == me.Gid {
			return
		}
	}
	t.Errorf("groups of uid %s = %v, missing primary %s", me.Uid, ids, me.Gid)
}
