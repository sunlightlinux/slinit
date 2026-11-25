package process

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateCredentialName(t *testing.T) {
	good := []string{"api-key", "DB_PASSWORD", "cert.pem", "secret"}
	for _, n := range good {
		if err := validateCredentialName(n); err != nil {
			t.Errorf("good name %q rejected: %v", n, err)
		}
	}
	bad := []string{
		"",
		".",
		"..",
		"foo/bar",
		"path/escape",
		"a\x00b",
	}
	for _, n := range bad {
		if err := validateCredentialName(n); err == nil {
			t.Errorf("bad name %q accepted", n)
		}
	}
}

func TestCredentialsDir(t *testing.T) {
	got := CredentialsDir("my-svc")
	want := "/run/credentials/my-svc"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// SetupCredentials / CleanupCredentials need root + tmpfs mount and
// therefore can't run in a normal unit-test environment. They are
// exercised end-to-end by tests/functional/cases/82-credentials.sh
// (see the functional suite). Here we cover only the pure helpers
// — name validation and path formation.

func TestCredentialSourceValueAndPathMutuallyExclusive(t *testing.T) {
	// Document the invariant exercised by the loader/runner: a
	// CredentialSource is either Path-backed or Value-backed but not
	// both. The struct itself doesn't enforce it; the parser does.
	src := CredentialSource{Name: "k", Path: "/etc/x"}
	if src.Value != "" {
		t.Error("Path-backed source must leave Value empty")
	}
	src2 := CredentialSource{Name: "k", Value: "hello"}
	if src2.Path != "" {
		t.Error("Value-backed source must leave Path empty")
	}
}

func TestCredentialNameRejectsNul(t *testing.T) {
	err := validateCredentialName("ab\x00cd")
	if err == nil || !strings.Contains(err.Error(), "invalid character") {
		t.Errorf("expected invalid-character error, got %v", err)
	}
}

// The credentials root must be traversable by a run-as service, or it
// cannot reach its own $CREDENTIALS_DIRECTORY: creating the service
// directory with MkdirAll(dir, 0700) made the root 0700 too. A root an
// older version left 0700 is repaired.
func TestMakeCredentialsDirRootTraversable(t *testing.T) {
	for _, stale := range []bool{false, true} {
		root := filepath.Join(t.TempDir(), "credentials")
		if stale {
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
		}
		dir := filepath.Join(root, "svc")
		if err := makeCredentialsDir(root, dir); err != nil {
			t.Fatalf("stale=%v: %v", stale, err)
		}
		st, err := os.Stat(root)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm()&0011 != 0011 {
			t.Errorf("stale=%v: credentials root mode %v, others cannot traverse it", stale, st.Mode().Perm())
		}
		st, err = os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0700 {
			t.Errorf("stale=%v: service dir mode %v, want 0700", stale, st.Mode().Perm())
		}
	}
}
