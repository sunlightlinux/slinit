package service

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// TestPredPathIsSocketPresent covers the S_ISSOCK positive path: a
// real Unix domain socket file (created via net.Listen("unix"))
// satisfies the predicate. Uses os.Pipe → unix socket via listener
// to stay pure-Go.
func TestPredPathIsSocketPresent(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "s.sock")
	// Create a real unix socket to exercise S_ISSOCK.
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Skipf("cannot create unix socket in tempdir: %v", err)
	}
	defer l.Close()

	p := Predicate{Kind: PredPathIsSocket, Param: sockPath}
	ok, why := p.Evaluate()
	if !ok {
		t.Errorf("expected socket predicate to succeed, got false: %s", why)
	}
}

func TestPredPathIsSocketRegularFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plain")
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	p := Predicate{Kind: PredPathIsSocket, Param: path}
	ok, _ := p.Evaluate()
	if ok {
		t.Error("regular file should not satisfy path-is-socket")
	}
}

func TestPredPathIsSocketMissing(t *testing.T) {
	p := Predicate{Kind: PredPathIsSocket, Param: "/nonexistent/sock"}
	ok, _ := p.Evaluate()
	if ok {
		t.Error("missing path should not satisfy path-is-socket")
	}
}

// TestFractionRejectsMalformed pins the parse-time error surface —
// callers see a specific reason rather than a generic false.
func TestFractionRejectsMalformed(t *testing.T) {
	cases := []string{
		"no-colon",
		"tag:not-a-number",
		"tag:150",  // > 100
		"tag:-10",  // negative
	}
	for _, c := range cases {
		ok, why := checkFraction(c)
		if ok {
			t.Errorf("%q: expected false", c)
		}
		if why == "" {
			t.Errorf("%q: expected a reason string", c)
		}
	}
}

// TestFractionZeroPercentAlwaysFalse verifies the 0% edge — no bucket
// hash can be < 0.0, so a 0% rollout never matches. Symmetric with
// 100% which always matches (bucket ranges [0, 100)).
func TestFractionZeroPercent(t *testing.T) {
	// Best-effort: we can't force /etc/machine-id in tests, but the
	// bucket math itself is deterministic given a machine-id. Just
	// verify the parse and range gates work. The actual bucket depends
	// on host machine-id which we don't mock here.
	if _, err := os.Stat("/etc/machine-id"); err != nil {
		t.Skip("no /etc/machine-id on this host; skipping bucket check")
	}
	ok, _ := checkFraction("test-rollout:0")
	if ok {
		t.Error("0% rollout should never match")
	}
	ok, _ = checkFraction("test-rollout:100")
	if !ok {
		t.Error("100% rollout should always match")
	}
}

// TestPredicateKindByNameNewKinds pins the new kebab names — a
// silent rename here would silently break every user's config.
func TestPredicateKindByNameNewKinds(t *testing.T) {
	if k, ok := PredicateKindByName("path-is-socket"); !ok || k != PredPathIsSocket {
		t.Errorf("path-is-socket: got (%v,%v) want (%v,true)", k, ok, PredPathIsSocket)
	}
	if k, ok := PredicateKindByName("fraction"); !ok || k != PredFraction {
		t.Errorf("fraction: got (%v,%v) want (%v,true)", k, ok, PredFraction)
	}
}
