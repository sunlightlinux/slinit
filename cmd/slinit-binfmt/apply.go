package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// applyResult accumulates what happened during an apply/unapply pass
// so the CLI can emit one summary line and pick an exit code that
// mirrors systemd-binfmt's semantics: non-zero only when at least
// one spec failed and none of the errors were "kernel module not
// loaded".
type applyResult struct {
	registered   int
	unregistered int
	skipped      int
	errors       []error
}

func (r *applyResult) String() string {
	return fmt.Sprintf("registered=%d unregistered=%d skipped=%d errors=%d",
		r.registered, r.unregistered, r.skipped, len(r.errors))
}

// binfmtMounted reports whether /proc/sys/fs/binfmt_misc/register
// exists. binfmt_misc is a kernel module + auto-mounted filesystem;
// on systems where it is not loaded (containers, minimal kernels),
// there is nothing meaningful for the tool to do.
func binfmtMounted() bool {
	_, err := os.Stat(registerPath)
	return err == nil
}

// registerSpec writes one spec to the kernel's register entry point.
// If a format with the same name is already registered, it is
// unregistered first (write "-1" to /proc/sys/fs/binfmt_misc/<name>)
// so this call always ends with the fresh definition in place.
func registerSpec(s spec) error {
	statusFile := filepath.Join(binfmtStatusDir, s.name)
	if _, err := os.Stat(statusFile); err == nil {
		if err := os.WriteFile(statusFile, []byte("-1"), 0); err != nil {
			return fmt.Errorf("unregister existing %q: %w", s.name, err)
		}
	}
	if err := os.WriteFile(registerPath, []byte(s.line), 0); err != nil {
		return fmt.Errorf("register %q: %w", s.name, err)
	}
	return nil
}

// unregisterAll walks every currently-registered format and writes
// "-1" to its status file. Skips the sentinel `register` entry and
// `status` entry, which are not real formats.
func unregisterAll() (*applyResult, error) {
	res := &applyResult{}
	entries, err := os.ReadDir(binfmtStatusDir)
	if err != nil {
		return res, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "register" || name == "status" {
			continue
		}
		path := filepath.Join(binfmtStatusDir, name)
		if err := os.WriteFile(path, []byte("-1"), 0); err != nil {
			res.errors = append(res.errors,
				fmt.Errorf("unregister %q: %w", name, err))
			continue
		}
		res.unregistered++
	}
	return res, nil
}

// applyFiles registers every spec extracted from paths. Files are
// processed in the order given so the caller (usually discover())
// controls override behaviour.
func applyFiles(paths []string) (*applyResult, error) {
	res := &applyResult{}
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			res.errors = append(res.errors, fmt.Errorf("open %s: %w", p, err))
			continue
		}
		specs, err := parseFile(f, p)
		f.Close()
		if err != nil {
			res.errors = append(res.errors, err)
			continue
		}
		for _, s := range specs {
			if err := registerSpec(s); err != nil {
				res.errors = append(res.errors, err)
				continue
			}
			res.registered++
		}
	}
	return res, nil
}
