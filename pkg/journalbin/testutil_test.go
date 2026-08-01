package journalbin

import "os"

// writeFileWithMode wraps os.WriteFile — kept as a helper so tests
// have one place to change if we ever need to inject a fake FS.
func writeFileWithMode(path string, data []byte, mode os.FileMode) error {
	return os.WriteFile(path, data, mode)
}
