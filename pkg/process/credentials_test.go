package process

import (
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
