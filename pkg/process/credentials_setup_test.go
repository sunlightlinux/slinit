package process

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// SetupCredentials as a service sees it: a read-only tmpfs at
// /run/credentials/<svc>, one 0400 file per source with the source's
// bytes, directory 0700 and owned by the service's user.
func TestSetupCredentialsPopulates(t *testing.T) {
	if !reexecAsRootWithMounts(t) {
		return
	}
	src := filepath.Join(t.TempDir(), "tls.key")
	if err := os.WriteFile(src, []byte("KEY\x00bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir, err := SetupCredentials("web", []CredentialSource{
		{Name: "tls.key", Path: src},
		{Name: "token", Value: "s3cret"},
		{Name: "empty", Value: ""},
	}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer CleanupCredentials("web")
	if dir != "/run/credentials/web" {
		t.Errorf("dir = %q", dir)
	}
	for name, want := range map[string]string{"tls.key": "KEY\x00bytes\n", "token": "s3cret", "empty": ""} {
		p := filepath.Join(dir, name)
		b, err := os.ReadFile(p)
		if err != nil || string(b) != want {
			t.Errorf("%s = %q, %v; want %q", name, b, err, want)
		}
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o400 {
			t.Errorf("%s mode %#o, want 0400", name, fi.Mode().Perm())
		}
		if st := fi.Sys().(*syscall.Stat_t); st.Uid != 0 || st.Gid != 0 {
			t.Errorf("%s owner %d:%d", name, st.Uid, st.Gid)
		}
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %#o, want 0700", di.Mode().Perm())
	}
	var sfs syscall.Statfs_t
	if err := syscall.Statfs(dir, &sfs); err != nil {
		t.Fatal(err)
	}
	if sfs.Flags&syscall.MS_RDONLY == 0 || sfs.Flags&syscall.MS_NOEXEC == 0 ||
		sfs.Flags&syscall.MS_NOSUID == 0 || sfs.Flags&syscall.MS_NODEV == 0 {
		t.Errorf("mount flags %#x, want ro,nosuid,nodev,noexec", sfs.Flags)
	}
	if sfs.Type != 0x01021994 { // TMPFS_MAGIC
		t.Errorf("fs type %#x, want tmpfs", sfs.Type)
	}
	// Read-only for root too: the service cannot change its credentials.
	if err := os.WriteFile(filepath.Join(dir, "new"), nil, 0o400); !errors.Is(err, syscall.EROFS) {
		t.Errorf("write into credentials dir: %v, want EROFS", err)
	}
}

func TestSetupCredentialsNoSources(t *testing.T) {
	dir, err := SetupCredentials("never-created", nil, 0, 0)
	if dir != "" || err != nil {
		t.Errorf("no sources: %q, %v; want \"\", nil", dir, err)
	}
	if _, err := os.Stat(CredentialsDir("never-created")); err == nil {
		t.Error("directory made for a service with no credentials")
	}
}

// A failure part-way tears the directory down: no half-populated tmpfs
// with the credentials written so far stays mounted.
func TestSetupCredentialsFailureTearsDown(t *testing.T) {
	if !reexecAsRootWithMounts(t) {
		return
	}
	for _, c := range []struct {
		name string
		src  []CredentialSource
		want string
	}{
		{"bad-name", []CredentialSource{{Name: "ok", Value: "first"}, {Name: "../escape", Value: "x"}},
			"contains invalid character"},
		{"missing-file", []CredentialSource{{Name: "ok", Value: "first"}, {Name: "f", Path: "/nonexistent/f"}},
			`credential "f": read /nonexistent/f: `},
		{"dot", []CredentialSource{{Name: "..", Value: "x"}}, "is reserved"},
	} {
		dir, err := SetupCredentials(c.name, c.src, 0, 0)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", c.name, err, c.want)
		}
		if dir != "" {
			t.Errorf("%s: dir %q returned with an error", c.name, dir)
		}
		if credentialsMounted(c.name) {
			t.Errorf("%s: tmpfs still mounted after the failure", c.name)
		}
		if _, err := os.Stat(CredentialsDir(c.name)); !os.IsNotExist(err) {
			t.Errorf("%s: directory left behind: %v", c.name, err)
		}
		if _, err := os.Stat(filepath.Join(CredentialsDir(c.name), "ok")); err == nil {
			t.Errorf("%s: an earlier credential is still readable", c.name)
		}
	}
}
