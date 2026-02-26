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
