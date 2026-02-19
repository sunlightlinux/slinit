// Package util provides internal utility functions for slinit.
package util

import (
	"path/filepath"
)

// CombinePaths combines a base path with a relative path.
// If the relative path is absolute, it is returned as-is.
func CombinePaths(base, rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(base, rel)
}

// ParentPath returns the parent directory of the given path.
func ParentPath(path string) string {
	return filepath.Dir(path)
}
