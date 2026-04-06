package autofs

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MountUnit describes a lazy mount point configuration.
type MountUnit struct {
	Name        string        // derived from filename (e.g., "home" from home.mount)
	Description string        // human-readable description
	What        string        // source (device, NFS path, etc.)
	Where       string        // absolute mount point path
	Type        string        // filesystem type (ext4, nfs, tmpfs, etc.)
	Options     string        // mount options (comma-separated)
	Timeout     time.Duration // idle timeout before auto-unmount (0 = never)
	AutofsType  string        // "indirect" (default) or "direct"
	DirMode     os.FileMode   // permissions for auto-created directories (default 0755)
	After       []string      // slinit services that must be running first
}

// ParseMountUnit parses a .mount configuration file.
// Format: key = value (same conventions as slinit service files).
func ParseMountUnit(r io.Reader, name string) (*MountUnit, error) {
	mu := &MountUnit{
		Name:       name,
		AutofsType: TypeIndirect,
		DirMode:    0755,
	}

	scanner := bufio.NewScanner(r)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || line[0] == '#' {
			continue
		}

		// Split on '=' or ':'
		key, value, op := splitSetting(line)
		if key == "" {
			return nil, fmt.Errorf("line %d: invalid syntax: %s", lineNum, line)
		}

		switch key {
		case "description":
			mu.Description = value
		case "what":
			mu.What = value
		case "where":
			mu.Where = value
		case "type":
			mu.Type = value
		case "options":
			mu.Options = value
		case "timeout":
			secs, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("line %d: invalid timeout: %s", lineNum, value)
			}
			mu.Timeout = time.Duration(secs) * time.Second
		case "autofs-type":
			if value != TypeIndirect && value != TypeDirect {
				return nil, fmt.Errorf("line %d: autofs-type must be 'indirect' or 'direct', got %q", lineNum, value)
			}
			mu.AutofsType = value
		case "directory-mode":
			mode, err := strconv.ParseUint(value, 8, 32)
			if err != nil {
				return nil, fmt.Errorf("line %d: invalid directory-mode: %s", lineNum, value)
			}
			mu.DirMode = os.FileMode(mode)
		case "after":
			if op == ':' {
				mu.After = append(mu.After, strings.Fields(value)...)
			} else {
				mu.After = append(mu.After, value)
			}
		default:
			return nil, fmt.Errorf("line %d: unknown setting %q", lineNum, key)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read error: %w", err)
	}

	return mu, nil
}

// splitSetting splits a line into key, value, and operator ('=' or ':').
func splitSetting(line string) (key, value string, op byte) {
	for i := 0; i < len(line); i++ {
		if line[i] == '=' || line[i] == ':' {
			op = line[i]
			key = strings.TrimSpace(line[:i])
			value = strings.TrimSpace(line[i+1:])
			return
		}
	}
	return "", "", 0
}

// ValidateMountUnit checks that required fields are present and valid.
func ValidateMountUnit(mu *MountUnit) error {
	if mu.Where == "" {
		return fmt.Errorf("mount unit %q: 'where' is required", mu.Name)
	}
	if !filepath.IsAbs(mu.Where) {
		return fmt.Errorf("mount unit %q: 'where' must be absolute path, got %q", mu.Name, mu.Where)
	}
	if mu.What == "" {
		return fmt.Errorf("mount unit %q: 'what' is required", mu.Name)
	}
	if mu.Type == "" {
		return fmt.Errorf("mount unit %q: 'type' is required", mu.Name)
	}
	return nil
}

// LoadMountUnits scans directories for .mount files and parses them.
func LoadMountUnits(dirs []string) ([]*MountUnit, error) {
	var units []*MountUnit

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read dir %s: %w", dir, err)
		}

		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".mount") {
				continue
			}

			path := filepath.Join(dir, e.Name())
			name := strings.TrimSuffix(e.Name(), ".mount")

			f, err := os.Open(path)
			if err != nil {
				return nil, fmt.Errorf("open %s: %w", path, err)
			}

			mu, err := ParseMountUnit(f, name)
			f.Close()
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", path, err)
			}

			if err := ValidateMountUnit(mu); err != nil {
				return nil, err
			}

			units = append(units, mu)
		}
	}

	return units, nil
}
