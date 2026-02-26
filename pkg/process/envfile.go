package process

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// ReadEnvFile reads a file of KEY=VALUE environment variable assignments.
// Lines starting with '#' and blank lines are skipped.
// Returns a map of key→value pairs.
func ReadEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open env-file: %w", err)
	}
	defer f.Close()

	env := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
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
