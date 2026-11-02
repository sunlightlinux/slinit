package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/process"
)

func writeCred(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(dir), 0o400); err != nil {
		t.Fatal(err)
	}
	return p
}

// import-credential searches the same stores condition-credential does,
// in systemd's order, the first store with a name wins, and an import
// never replaces an explicit credential of the same name.
func TestImportCredentialsSearchOrderAndPrecedence(t *testing.T) {
	root := t.TempDir()
	etc, run, usr := filepath.Join(root, "etc"), filepath.Join(root, "run"), filepath.Join(root, "usr")
	saved := process.CredentialStores
	process.CredentialStores = []string{etc, run, usr}
	t.Cleanup(func() { process.CredentialStores = saved })
	t.Setenv("SYSTEM_CREDSTORE", "")
	own := filepath.Join(root, "own")
	t.Setenv("CREDENTIALS_DIRECTORY", own)

	writeCred(t, run, "db.token")          // only in /run: found (was missed)
	etcAPI := writeCred(t, etc, "api.key") // in /etc and /usr: /etc wins
	writeCred(t, usr, "api.key")
	ownBoot := writeCred(t, own, "boot.secret") // manager's own beats the stores
	writeCred(t, etc, "boot.secret")
	writeCred(t, etc, "explicit") // an explicit credential keeps its name

	explicit := []process.CredentialSource{{Name: "explicit", Value: "set-credential value"}}
	got := importCredentials(explicit, []string{"*"})

	byName := map[string]process.CredentialSource{}
	for _, c := range got {
		if _, dup := byName[c.Name]; dup {
			t.Errorf("credential %q appears twice: an import must not shadow or duplicate a name", c.Name)
		}
		byName[c.Name] = c
	}
	if c, ok := byName["db.token"]; !ok || filepath.Dir(c.Path) != run {
		t.Errorf("db.token in /run/credstore not imported: %+v", c)
	}
	if byName["api.key"].Path != etcAPI {
		t.Errorf("api.key = %s, want the /etc copy %s", byName["api.key"].Path, etcAPI)
	}
	if byName["boot.secret"].Path != ownBoot {
		t.Errorf("boot.secret = %s, want slinit's own %s", byName["boot.secret"].Path, ownBoot)
	}
	if c := byName["explicit"]; c.Value != "set-credential value" || c.Path != "" {
		t.Errorf("explicit credential replaced by an import: %+v", c)
	}
}

func TestImportCredentialsOverrideAndNoGlobs(t *testing.T) {
	root := t.TempDir()
	one := filepath.Join(root, "one")
	writeCred(t, one, "x")
	writeCred(t, filepath.Join(root, "other"), "y")
	saved := process.CredentialStores
	process.CredentialStores = []string{filepath.Join(root, "other")}
	t.Cleanup(func() { process.CredentialStores = saved })
	t.Setenv("CREDENTIALS_DIRECTORY", "")

	// $SYSTEM_CREDSTORE replaces the store list with one directory.
	t.Setenv("SYSTEM_CREDSTORE", one)
	got := importCredentials(nil, []string{"*"})
	if len(got) != 1 || got[0].Name != "x" {
		t.Errorf("with SYSTEM_CREDSTORE=%s: %+v, want only x", one, got)
	}
	// No globs: the explicit list comes back as it was.
	explicit := []process.CredentialSource{{Name: "a", Value: "1"}}
	if got := importCredentials(explicit, nil); len(got) != 1 || got[0].Name != "a" {
		t.Errorf("no import-credential: %+v", got)
	}
}
