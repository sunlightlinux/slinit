package process

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env")
	content := `# Comment
FOO=bar
BAZ=qux
EMPTY=

# Another comment
MULTI_EQ=a=b=c
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	env, err := ReadEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]string{
		"FOO":       "bar",
		"BAZ":       "qux",
		"EMPTY":     "",
		"MULTI_EQ":  "a=b=c",
	}
	for k, want := range tests {
		got, ok := env[k]
		if !ok {
			t.Errorf("key %q not found", k)
		} else if got != want {
			t.Errorf("key %q: got %q, want %q", k, got, want)
		}
	}

	if len(env) != len(tests) {
		t.Errorf("expected %d entries, got %d", len(tests), len(env))
	}
}

func TestReadEnvFileNotFound(t *testing.T) {
	_, err := ReadEnvFile("/nonexistent/path")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestEnvFileClear(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env")
	content := "FOO=bar\nBAZ=qux\n!clear\nONLY=this\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	env, err := ReadEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := env["FOO"]; ok {
		t.Error("FOO should have been cleared")
	}
	if v, ok := env["ONLY"]; !ok || v != "this" {
		t.Errorf("ONLY: got %q, want 'this'", v)
	}
	if len(env) != 1 {
		t.Errorf("expected 1 entry, got %d", len(env))
	}
}

func TestEnvFileUnset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env")
	content := "A=1\nB=2\nC=3\n!unset A C\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	env, err := ReadEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := env["A"]; ok {
		t.Error("A should have been unset")
	}
	if _, ok := env["C"]; ok {
		t.Error("C should have been unset")
	}
	if v := env["B"]; v != "2" {
		t.Errorf("B: got %q, want '2'", v)
	}
}

func TestEnvFileImport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env")
	content := "LOCAL=yes\n!import IMPORTED_VAR MISSING_VAR\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	origEnv := []string{"IMPORTED_VAR=hello", "OTHER=ignored"}
	env, err := ReadEnvFileWithOrigEnv(path, origEnv)
	if err != nil {
		t.Fatal(err)
	}
	if v := env["LOCAL"]; v != "yes" {
		t.Errorf("LOCAL: got %q, want 'yes'", v)
	}
	if v := env["IMPORTED_VAR"]; v != "hello" {
		t.Errorf("IMPORTED_VAR: got %q, want 'hello'", v)
	}
	if _, ok := env["MISSING_VAR"]; ok {
		t.Error("MISSING_VAR should not be present")
	}
	if _, ok := env["OTHER"]; ok {
		t.Error("OTHER should not be imported")
	}
}
