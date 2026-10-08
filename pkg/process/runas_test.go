package process

import "testing"

func TestResolveRunAs(t *testing.T) {
	uid, gid, err := ResolveRunAs("root")
	if err != nil || uid != 0 || gid != 0 {
		t.Errorf("root: %d:%d, %v", uid, gid, err)
	}
	if uid, _, err := ResolveRunAs("0"); err != nil || uid != 0 {
		t.Errorf("numeric 0: %d, %v", uid, err)
	}
	if _, _, err := ResolveRunAs("nosuchuser-slinit-test"); err == nil {
		t.Error("unknown user resolved")
	}
	// A group that does not resolve is an error too; it used to fall
	// back to the user's primary group without a word.
	if _, _, err := ResolveRunAs("root:nosuchgroup-slinit-test"); err == nil {
		t.Error("unknown group resolved")
	}
	if _, _, err := ResolveRunAs(""); err == nil {
		t.Error("empty spec resolved")
	}
}
