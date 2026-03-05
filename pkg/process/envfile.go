package process

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

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
	f, err := os.Open(path)
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
