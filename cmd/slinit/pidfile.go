package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeOwnPidFile records this process's PID at path.
//
// Written to a temporary file in the same directory and renamed, so a
// reader never sees a half-written or empty file: the whole point of
// the file is that something else polls it, and an empty read looks
// exactly like "not running yet".
func writeOwnPidFile(path string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".slinit-pid-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := fmt.Fprintf(tmp, "%d\n", os.Getpid()); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
