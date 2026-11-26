package main

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// --user ran the daemon with no supplementary groups at all: a bare
// Credential makes Go call setgroups(0). OpenRC calls initgroups(3), so
// the daemon has the user's groups. As an unprivileged user that same
// setgroups(0) failed with EPERM and --user <self> could not start
// anything.
func TestDaemonGetsUserGroups(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	ids, err := me.GroupIds()
	if err != nil {
		t.Skip(err)
	}
	want := map[string]bool{me.Gid: true}
	for _, id := range ids {
		want[id] = true
	}
	if os.Getuid() != 0 {
		// Unprivileged, the daemon can only get the groups we have.
		cur, _ := os.Getgroups()
		have := map[string]bool{}
		for _, g := range cur {
			have[strconv.Itoa(g)] = true
		}
		for g := range want {
			if !have[g] {
				t.Skipf("our groups %v are not %s's %v", cur, me.Username, ids)
			}
		}
	}

	out := filepath.Join(t.TempDir(), "status")
	opts := Options{
		Service: "test",
		User:    me.Username,
		Exec:    sh,
		Args:    []string{"-c", `cat /proc/self/status > "$0"`, out},
	}
	_, exitCh, err := startDaemon(opts)
	if err != nil {
		t.Fatalf("startDaemon --user %s: %v", me.Username, err)
	}
	<-exitCh
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "Groups:"); ok {
			got = strings.Fields(rest)
		}
	}
	var wantList []string
	for g := range want {
		wantList = append(wantList, g)
	}
	sort.Strings(got)
	sort.Strings(wantList)
	if strings.Join(got, ",") != strings.Join(wantList, ",") {
		t.Errorf("daemon groups = %v, want %v", got, wantList)
	}
}

// --group alone changes the gid and keeps the supplementary set.
func TestDaemonCredentialGroupOnly(t *testing.T) {
	cred, err := daemonCredential(Options{Group: strconv.Itoa(os.Getgid())})
	if err != nil {
		t.Fatal(err)
	}
	if !cred.NoSetGroups || cred.Groups != nil {
		t.Errorf("--group alone: %+v, want the supplementary set kept", cred)
	}
	// A numeric uid with no passwd entry still gets its primary gid.
	cred, err = daemonCredential(Options{User: "4000000001", Group: "4000000002"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cred.Groups) != 1 || cred.Groups[0] != 4000000002 {
		t.Errorf("unknown uid: groups %v, want [4000000002]", cred.Groups)
	}
}
