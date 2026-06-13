package process

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// credentialsRoot is the base directory under which slinit mounts a
// fresh tmpfs per service for $CREDENTIALS_DIRECTORY. Matches systemd's
// layout so unit files port without surprises.
const credentialsRoot = "/run/credentials"

// CredentialSource is one credential the daemon should make available
// inside the service's $CREDENTIALS_DIRECTORY. Exactly one of Path or
// Value is set: Path = copy a file from disk, Value = inline literal.
type CredentialSource struct {
	Name  string // file name created under $CREDENTIALS_DIRECTORY
	Path  string // load-credential = NAME:PATH source
	Value string // set-credential = NAME:VALUE literal
}

// CredentialsDir returns the per-service credentials directory path
// (created and populated by SetupCredentials, removed by
// CleanupCredentials). Exposed to the service as $CREDENTIALS_DIRECTORY.
func CredentialsDir(serviceName string) string {
	return filepath.Join(credentialsRoot, serviceName)
}

// SetupCredentials prepares the service's $CREDENTIALS_DIRECTORY: a
// fresh tmpfs (size=1M, mode=0700) mounted at /run/credentials/<svc>/,
// populated from sources, files chmod 0400 chown to (uid,gid), then
// the tmpfs remounted read-only. Returns the directory path on success
// so the caller can wire it into the service's environment.
//
// If any step fails the partially-prepared directory is torn down to
// avoid leaking half-populated credentials between starts.
func SetupCredentials(serviceName string, sources []CredentialSource, uid, gid uint32) (string, error) {
	if len(sources) == 0 {
		return "", nil
	}
	dir := CredentialsDir(serviceName)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("credentials mkdir %s: %w", dir, err)
	}
	// Fresh tmpfs each time. If a prior mount lingered (daemon crash),
	// the new MS_NOSUID|MS_NODEV|MS_NOEXEC mount stacks on top — the
	// kernel keeps the old one but the service only sees the new one.
	// Cleanup below umounts everything at the same path.
	mountOpts := "size=1M,mode=0700"
	if err := unix.Mount("tmpfs", dir, "tmpfs",
		unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, mountOpts); err != nil {
		return "", fmt.Errorf("credentials mount tmpfs %s: %w", dir, err)
	}

	// Best-effort cleanup helper for the failure paths below.
	teardown := func() {
		_ = unix.Unmount(dir, unix.MNT_DETACH)
		_ = os.RemoveAll(dir)
	}

	for _, s := range sources {
		if err := validateCredentialName(s.Name); err != nil {
			teardown()
			return "", err
		}
		dst := filepath.Join(dir, s.Name)
		var data []byte
		if s.Path != "" {
			b, err := os.ReadFile(s.Path)
			if err != nil {
				teardown()
				return "", fmt.Errorf("credential %q: read %s: %w", s.Name, s.Path, err)
			}
			data = b
		} else {
			data = []byte(s.Value)
		}
		if err := os.WriteFile(dst, data, 0400); err != nil {
			teardown()
			return "", fmt.Errorf("credential %q: write: %w", s.Name, err)
		}
		if err := os.Chown(dst, int(uid), int(gid)); err != nil {
			// Non-root daemons (user-instance / container) can't chown.
			// Tolerate EPERM/EINVAL — the tmpfs already restricts to
			// mode 0400 and the dir is mode 0700 owned by us.
			if !os.IsPermission(err) {
				teardown()
				return "", fmt.Errorf("credential %q: chown: %w", s.Name, err)
			}
		}
	}

	// Chown the directory to the service so it can read its own
	// credentials. Same EPERM tolerance as files above.
	if err := os.Chown(dir, int(uid), int(gid)); err != nil && !os.IsPermission(err) {
		teardown()
		return "", fmt.Errorf("credentials chown %s: %w", dir, err)
	}

	// Lock down: remount read-only so the service cannot mutate its
	// own credentials at runtime. Failure here is fatal: a writable
	// credentials dir defeats the point.
	if err := unix.Mount("", dir, "", unix.MS_REMOUNT|unix.MS_RDONLY|
		unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, mountOpts); err != nil {
		teardown()
		return "", fmt.Errorf("credentials remount ro %s: %w", dir, err)
	}
	return dir, nil
}

// CleanupCredentials undoes SetupCredentials: umount the tmpfs and
// remove the directory. Safe to call on a non-existent path. Errors
// other than ENOENT are returned for the caller to log; the runtime
// directory is best-effort cleanup.
func CleanupCredentials(serviceName string) error {
	dir := CredentialsDir(serviceName)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	// MNT_DETACH lets us umount even if a child still has a fd open
	// (the mount stays alive until that fd closes, but the path is
	// freed for new mounts).
	if err := unix.Unmount(dir, unix.MNT_DETACH); err != nil &&
		err != unix.EINVAL && err != unix.ENOENT {
		return fmt.Errorf("umount %s: %w", dir, err)
	}
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", dir, err)
	}
	return nil
}

// validateCredentialName rejects names that would let a service write
// outside its credentials directory. Slashes split path components and
// "." / ".." escape upward, both forbidden.
func validateCredentialName(name string) error {
	if name == "" {
		return fmt.Errorf("credential name is empty")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("credential name %q is reserved", name)
	}
	for _, r := range name {
		if r == '/' || r == 0 {
			return fmt.Errorf("credential name %q contains invalid character", name)
		}
	}
	return nil
}
