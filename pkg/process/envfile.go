package process

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// openRegular opens path for reading and refuses anything but a
// regular file. These files are read by PID 1 with the service set's
// lock held: a FIFO would block the open until something opened its
// other end, and a device such as /dev/zero would be read without end.
// O_NONBLOCK makes the open of a FIFO return at once; it has no effect
// on the read of a regular file.
func openRegular(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() && !isDevNull(fi) {
		f.Close()
		return nil, fmt.Errorf("%s: not a regular file (%s)", path, fi.Mode().Type())
	}
	return f, nil
}

// isDevNull reports whether fi is /dev/null, which every reader of
// these files has always taken as an empty file (an env-dir entry
// linked to it unsets the variable, as in runit).
func isDevNull(fi os.FileInfo) bool {
	null, err := os.Stat(os.DevNull)
	return err == nil && os.SameFile(fi, null)
}

// ReadEnvFile reads a file of KEY=VALUE environment variable assignments.
// Lines starting with '#' and blank lines are skipped.
// Supports meta-commands: !clear, !unset VAR..., !import VAR...
// Returns a map of key→value pairs.
func ReadEnvFile(path string) (map[string]string, error) {
	return ReadEnvFileWithOrigEnv(path, nil)
}

// ReadEnvFileWithOrigEnv reads an env-file with support for meta-commands
// that reference the original (parent) environment.
//
// Meta-commands (dinit-compatible):
//   - !clear         — clear all previously set variables
//   - !unset VAR ... — unset specific variables
//   - !import VAR ...— import specific variables from origEnv
//
// origEnv is the original environment before any env-file processing.
// If nil, os.Environ() is used as fallback for !import.
func ReadEnvFileWithOrigEnv(path string, origEnv []string) (map[string]string, error) {
	return readEnvFile(path, origEnv, openRegular)
}

// ReadStartupEnvFile reads slinit's own --env-file. That is read once,
// before any service is managed and with no lock held, so unlike a
// service's env-file it may be a pipe (`slinit -u --env-file <(...)`)
// and is opened as a plain file.
func ReadStartupEnvFile(path string) (map[string]string, error) {
	return readEnvFile(path, nil, os.Open)
}

func readEnvFile(path string, origEnv []string, open func(string) (*os.File, error)) (map[string]string, error) {
	f, err := open(path)
	if err != nil {
		return nil, fmt.Errorf("open env-file: %w", err)
	}
	defer f.Close()

	// Build lookup for original environment
	orig := make(map[string]string)
	if origEnv == nil {
		origEnv = os.Environ()
	}
	for _, e := range origEnv {
		if idx := strings.IndexByte(e, '='); idx > 0 {
			orig[e[:idx]] = e[idx+1:]
		}
	}

	env := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Meta-commands
		if strings.HasPrefix(line, "!") {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			switch fields[0] {
			case "!clear":
				// Clear all previously set variables
				env = make(map[string]string)
			case "!unset":
				// Unset specific variables
				for _, varName := range fields[1:] {
					delete(env, varName)
				}
			case "!import":
				// Import specific variables from original environment
				for _, varName := range fields[1:] {
					if val, ok := orig[varName]; ok {
						env[varName] = val
					}
				}
			default:
				// Unknown meta-command, skip
			}
			continue
		}

		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue // skip lines without '='
		}
		key := line[:idx]
		value := line[idx+1:]
		env[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read env-file: %w", err)
	}
	return env, nil
}

// ReadEnvDir reads environment variables from a directory (runit envdir style).
// Each file in the directory defines one variable:
//   - File name = variable name
//   - First line of file content = variable value
//   - Empty file = unset the variable (returned with empty value and unset=true)
//   - Trailing whitespace is trimmed from values
//   - NUL bytes in values are replaced with newlines
//
// Returns a map of key→value pairs. Empty values mean "unset this var".
func ReadEnvDir(dirPath string) (map[string]string, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, fmt.Errorf("read env-dir: %w", err)
	}

	env := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Skip hidden files and files with '=' in name
		if strings.HasPrefix(name, ".") || strings.ContainsRune(name, '=') {
			continue
		}

		filePath := filepath.Join(dirPath, name)
		f, err := openRegular(filePath)
		if err != nil {
			continue // skip unreadable files
		}
		data, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			continue
		}

		if len(data) == 0 {
			// Empty file means unset
			env[name] = ""
			continue
		}

		// Take first line only
		value := string(data)
		if idx := strings.IndexByte(value, '\n'); idx >= 0 {
			value = value[:idx]
		}
		// Replace NUL bytes with newlines (runit convention)
		value = strings.ReplaceAll(value, "\x00", "\n")
		// Trim trailing whitespace
		value = strings.TrimRight(value, " \t")

		env[name] = value
	}
	return env, nil
}
