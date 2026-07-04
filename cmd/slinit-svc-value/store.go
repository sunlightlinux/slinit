package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// defaultRuntimeDir is where slinit keeps its per-service state.
// Matches the rest of the codebase (pkg/service/process.go's notify
// socket, pkg/snapshot's default path, etc.). Kept as a var so tests
// can point at a scratch tree.
var defaultRuntimeDir = "/run/slinit"

// store owns the "one file per key" convention: under
// $RC_SVCDIR/options/$SVC/, each stored value lives in a file whose
// basename is the key. That layout preserves compat with OpenRC's
// librc — an init.d script that reads/writes via `service_get_value`
// / `service_set_value` sees the same on-disk shape.
type store struct {
	root    string // "options" root
	service string // service name, used as the subdir
}

// newStore reads the service name and store root from the
// environment. RC_SVCNAME is honoured first (OpenRC convention),
// SLINIT_SERVICENAME as a fallback (native slinit env var). RC_SVCDIR
// overrides the runtime dir so init.d wrappers that already set it
// keep pointing at their expected location.
func newStore() (*store, error) {
	svc := firstNonEmpty(os.Getenv("RC_SVCNAME"), os.Getenv("SLINIT_SERVICENAME"))
	if svc == "" {
		return nil, fmt.Errorf("service name required (set RC_SVCNAME or SLINIT_SERVICENAME)")
	}
	root := firstNonEmpty(os.Getenv("RC_SVCDIR"), defaultRuntimeDir)
	return &store{
		root:    filepath.Join(root, "options"),
		service: svc,
	}, nil
}

// path resolves the file backing a single key. Callers use
// filepath.Base on user-supplied keys before calling; the extra
// Clean here is defensive against `..` sneaking through.
func (s *store) path(key string) string {
	return filepath.Join(s.root, s.service, filepath.Base(key))
}

// Get reads the stored value for key. ok=false when the file does
// not exist — the caller propagates that as exit code 1 to match
// OpenRC's service_get_value behaviour.
func (s *store) Get(key string) (string, bool, error) {
	if err := validateKey(key); err != nil {
		return "", false, err
	}
	buf, err := os.ReadFile(s.path(key))
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(buf), true, nil
}

// Set writes value under key, creating parent dirs as needed. An
// empty value deletes the file entirely — OpenRC's C original does
// the same via `rc_service_value_set(svc, k, NULL)`.
func (s *store) Set(key, value string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	target := s.path(key)
	if value == "" {
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	// Write verbatim, no trailing newline: `service_get_value`
	// consumers concatenate it into shell expressions, and any tail
	// whitespace would show up in the reconstructed value.
	return os.WriteFile(target, []byte(value), 0644)
}

// Export mirrors OpenRC's service_export: for each var, if it is
// not already stored, capture its current environment value. A var
// that is unset in the environment is skipped with a warning-style
// message the caller may choose to emit.
func (s *store) Export(vars []string) []string {
	var missing []string
	for _, v := range vars {
		if v == "" {
			continue
		}
		if _, ok, err := s.Get(v); err == nil && ok {
			continue
		}
		val, present := os.LookupEnv(v)
		if !present {
			missing = append(missing, v)
			continue
		}
		_ = s.Set(v, val)
	}
	return missing
}

// validateKey rejects the two shapes that would clearly break the
// file-per-key backing: an empty key, and a key containing a path
// separator or '..' component that could escape the service dir.
func validateKey(key string) error {
	if key == "" {
		return fmt.Errorf("empty key")
	}
	if strings.ContainsAny(key, "/\x00") {
		return fmt.Errorf("key %q contains illegal characters", key)
	}
	if key == "." || key == ".." {
		return fmt.Errorf("key %q is a path traversal", key)
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
