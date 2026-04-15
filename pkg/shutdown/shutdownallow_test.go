package shutdown

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/utmp"
)

func TestLoadShutdownAllow_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shutdown.allow")
	content := "# comment line\n" +
		"root\n" +
		"\n" +
		"  operator  \n" +
		"admin # trailing comment\n" +
		"# another comment\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadShutdownAllow(path)
	if err != nil {
		t.Fatalf("LoadShutdownAllow: %v", err)
	}
	want := []string{"root", "operator", "admin"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLoadShutdownAllow_Missing(t *testing.T) {
	got, err := LoadShutdownAllow("/nonexistent/slinit/shutdown.allow")
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if got != nil {
		t.Errorf("missing file should return nil, got %v", got)
	}
}

func TestLoadShutdownAllow_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shutdown.allow")
	if err := os.WriteFile(path, []byte("# only comments\n\n#\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadShutdownAllow(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("comment-only file should yield nil, got %v", got)
	}
}

func TestFindShutdownAllow(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	if err := os.WriteFile(b, []byte("root\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// a missing, b present → returns b
	if got := FindShutdownAllow([]string{a, b}); got != b {
		t.Errorf("got %q, want %q", got, b)
	}

	// both missing → empty
	if got := FindShutdownAllow([]string{a, a + "2"}); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestCheckShutdownAllow_NoPath(t *testing.T) {
	// No path → access control disabled → allowed.
	allowed, gated := CheckShutdownAllow("", nil)
	if !allowed || gated {
		t.Errorf("empty path → allowed=%v gated=%v, want true/false", allowed, gated)
	}
}

func TestCheckShutdownAllow_MissingFile(t *testing.T) {
	allowed, gated := CheckShutdownAllow("/does/not/exist", nil)
	if !allowed || gated {
		t.Errorf("missing file → allowed=%v gated=%v, want true/false", allowed, gated)
	}
}

func TestCheckShutdownAllow_EmptyDenies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shutdown.allow")
	if err := os.WriteFile(path, []byte("# nobody\n"), 0644); err != nil {
		t.Fatal(err)
	}
	allowed, gated := CheckShutdownAllow(path, logging.New(logging.LevelDebug))
	if allowed || !gated {
		t.Errorf("empty file → allowed=%v gated=%v, want false/true", allowed, gated)
	}
}

func TestCheckShutdownAllow_AuthorisedUserLoggedIn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shutdown.allow")
	if err := os.WriteFile(path, []byte("root\noperator\n"), 0644); err != nil {
		t.Fatal(err)
	}

	orig := listSessionsFunc
	t.Cleanup(func() { listSessionsFunc = orig })
	listSessionsFunc = func() []utmp.Session {
		return []utmp.Session{
			{User: "alice", Line: "pts/0"},
			{User: "operator", Line: "tty2"},
		}
	}

	allowed, gated := CheckShutdownAllow(path, logging.New(logging.LevelDebug))
	if !allowed || !gated {
		t.Errorf("authorised user logged in → allowed=%v gated=%v, want true/true", allowed, gated)
	}
}

func TestCheckShutdownAllow_NoAuthorisedUserLoggedIn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shutdown.allow")
	if err := os.WriteFile(path, []byte("root\noperator\n"), 0644); err != nil {
		t.Fatal(err)
	}

	orig := listSessionsFunc
	t.Cleanup(func() { listSessionsFunc = orig })
	listSessionsFunc = func() []utmp.Session {
		return []utmp.Session{
			{User: "alice", Line: "pts/0"},
			{User: "bob", Line: "tty2"},
		}
	}

	allowed, gated := CheckShutdownAllow(path, logging.New(logging.LevelDebug))
	if allowed || !gated {
		t.Errorf("no authorised user → allowed=%v gated=%v, want false/true", allowed, gated)
	}
}

func TestCheckShutdownAllow_NoSessionsAtAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shutdown.allow")
	if err := os.WriteFile(path, []byte("root\n"), 0644); err != nil {
		t.Fatal(err)
	}

	orig := listSessionsFunc
	t.Cleanup(func() { listSessionsFunc = orig })
	listSessionsFunc = func() []utmp.Session { return nil }

	allowed, gated := CheckShutdownAllow(path, logging.New(logging.LevelDebug))
	if allowed || !gated {
		t.Errorf("no sessions → allowed=%v gated=%v, want false/true", allowed, gated)
	}
}

func TestCheckShutdownAllow_NilLoggerSafe(t *testing.T) {
	// Must not panic even when called with a nil logger.
	CheckShutdownAllow("", nil)
	CheckShutdownAllow("/does/not/exist", nil)

	dir := t.TempDir()
	path := filepath.Join(dir, "shutdown.allow")
	os.WriteFile(path, []byte("root\n"), 0644)

	orig := listSessionsFunc
	t.Cleanup(func() { listSessionsFunc = orig })
	listSessionsFunc = func() []utmp.Session { return nil }
	CheckShutdownAllow(path, nil)
}
