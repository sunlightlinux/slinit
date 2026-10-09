package main

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

// immortalConfig is the subset of immortal's run.yml that maps onto
// slinit. Fields it does not have are reported as warnings rather than
// dropped, so an operator reading stderr knows what still needs a hand.
type immortalConfig struct {
	Cmd        string
	Cwd        string
	User       string
	Logger     string
	RequireCmd string
	PostExit   string
	Wait       int  // seconds before the first start
	Retries    int  // -1 forever, 0 once, N times
	HasRetries bool // distinguishes "retries: 0" from absent

	Env     map[string]string
	EnvKeys []string // insertion order, so output is stable

	Require []string

	Log    logBlock
	Stderr logBlock

	PidFollow string
	PidParent string
	PidChild  string
}

type logBlock struct {
	File      string
	Age       int // seconds
	Num       int
	Size      int // MegaBytes
	Timestamp bool
	set       bool
}

// parseImmortalYAML reads the flat subset of YAML that immortal's
// run.yml uses: top-level `key: value`, three nested blocks (log,
// stderr, pid), one string map (env) and one list (require).
//
// Hand-written rather than pulled in through gopkg.in/yaml.v3 on
// purpose. slinit's module carries two third-party dependencies
// (godbus, x/sys) and this is PID 1's module; a general YAML parser is
// a large surface to add for one converter of a niche format. The other
// four converters hand-read their inputs too — runit directories,
// OpenRC shell, sysvinit scripts — so this matches the house style
// rather than inventing one.
//
// What it deliberately does not implement: anchors, aliases, multi-line
// scalars, flow sequences, nested lists, quoted keys, documents.
// immortal's own README example and its Config struct use none of them.
// Anything unrecognised is reported, never guessed at.
func parseImmortalYAML(r *bufio.Scanner) (*immortalConfig, []warning, error) {
	cfg := &immortalConfig{Retries: -1, Env: map[string]string{}}
	var warns []warning

	// block is the nested map currently being read ("log", "stderr",
	// "pid", "env"), or "" at the top level. immortal nests exactly one
	// level deep, so one variable is enough.
	block := ""
	lineNo := 0

	for r.Scan() {
		lineNo++
		raw := r.Text()
		line := strings.TrimRight(raw, " \t")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "---") {
			continue
		}

		indented := line != strings.TrimLeft(line, " \t")
		trimmed := strings.TrimSpace(line)

		// A list item belongs to whatever key opened the list.
		if strings.HasPrefix(trimmed, "- ") || trimmed == "-" {
			item := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			item = unquote(item)
			if block == "require" && item != "" {
				cfg.Require = append(cfg.Require, item)
			} else if item != "" {
				warns = append(warns, warning{"WARN", fmt.Sprintf(
					"line %d: list item %q outside a list slinit maps; ignored", lineNo, item)})
			}
			continue
		}

		key, val, ok := splitKeyValue(trimmed)
		if !ok {
			warns = append(warns, warning{"WARN", fmt.Sprintf(
				"line %d: cannot read %q as `key: value`; ignored", lineNo, trimmed)})
			continue
		}

		if !indented {
			// A new top-level key ends any block.
			block = ""
		}

		if indented && block != "" {
			if err := assignNested(cfg, block, key, val, lineNo, &warns); err != nil {
				return nil, warns, err
			}
			continue
		}

		// Top-level key. An empty value opens a block or a list.
		if val == "" {
			switch key {
			case "log", "stderr", "pid", "env", "require":
				block = key
			default:
				warns = append(warns, warning{"WARN", fmt.Sprintf(
					"line %d: `%s:` has no value and is not a block slinit knows; ignored",
					lineNo, key)})
			}
			continue
		}

		if err := assignTop(cfg, key, val, lineNo, &warns); err != nil {
			return nil, warns, err
		}
	}
	if err := r.Err(); err != nil {
		return nil, warns, err
	}
	if cfg.Cmd == "" {
		return nil, warns, fmt.Errorf("no `cmd:` — immortal requires one and so does the conversion")
	}
	return cfg, warns, nil
}

func assignTop(cfg *immortalConfig, key, val string, lineNo int, warns *[]warning) error {
	switch key {
	case "cmd":
		cfg.Cmd = unquote(val)
	case "cwd":
		cfg.Cwd = unquote(val)
	case "user":
		cfg.User = unquote(val)
	case "logger":
		cfg.Logger = unquote(val)
	case "require_cmd":
		cfg.RequireCmd = unquote(val)
	case "post_exit":
		cfg.PostExit = unquote(val)
	case "wait":
		n, err := strconv.Atoi(unquote(val))
		if err != nil {
			return fmt.Errorf("line %d: wait: %w", lineNo, err)
		}
		cfg.Wait = n
	case "retries":
		n, err := strconv.Atoi(unquote(val))
		if err != nil {
			return fmt.Errorf("line %d: retries: %w", lineNo, err)
		}
		cfg.Retries = n
		cfg.HasRetries = true
	default:
		*warns = append(*warns, warning{"NOTE", fmt.Sprintf(
			"line %d: `%s` is not a key slinit maps; ignored", lineNo, key)})
	}
	return nil
}

func assignNested(cfg *immortalConfig, block, key, val string, lineNo int, warns *[]warning) error {
	switch block {
	case "env":
		v := unquote(val)
		if _, seen := cfg.Env[key]; !seen {
			cfg.EnvKeys = append(cfg.EnvKeys, key)
		}
		cfg.Env[key] = v
		return nil
	case "pid":
		switch key {
		case "follow":
			cfg.PidFollow = unquote(val)
		case "parent":
			cfg.PidParent = unquote(val)
		case "child":
			cfg.PidChild = unquote(val)
		default:
			*warns = append(*warns, warning{"NOTE", fmt.Sprintf(
				"line %d: pid.%s is not a key slinit maps; ignored", lineNo, key)})
		}
		return nil
	case "log", "stderr":
		lb := &cfg.Log
		if block == "stderr" {
			lb = &cfg.Stderr
		}
		lb.set = true
		switch key {
		case "file":
			lb.File = unquote(val)
		case "age":
			n, err := strconv.Atoi(unquote(val))
			if err != nil {
				return fmt.Errorf("line %d: %s.age: %w", lineNo, block, err)
			}
			lb.Age = n
		case "num":
			n, err := strconv.Atoi(unquote(val))
			if err != nil {
				return fmt.Errorf("line %d: %s.num: %w", lineNo, block, err)
			}
			lb.Num = n
		case "size":
			n, err := strconv.Atoi(unquote(val))
			if err != nil {
				return fmt.Errorf("line %d: %s.size: %w", lineNo, block, err)
			}
			lb.Size = n
		case "timestamp":
			lb.Timestamp = unquote(val) == "true" || unquote(val) == "yes"
		default:
			*warns = append(*warns, warning{"NOTE", fmt.Sprintf(
				"line %d: %s.%s is not a key slinit maps; ignored", lineNo, block, key)})
		}
		return nil
	case "require":
		// `require:` opened a list, but this line is `key: value`.
		*warns = append(*warns, warning{"WARN", fmt.Sprintf(
			"line %d: require expects a list of service names, got `%s:`; ignored",
			lineNo, key)})
		return nil
	}
	return nil
}

// splitKeyValue splits on the first colon, which is all immortal's
// grammar needs: its values are paths, commands and numbers, and a
// command containing a colon still has its key before the first one.
func splitKeyValue(s string) (key, val string, ok bool) {
	i := strings.IndexByte(s, ':')
	if i <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(s[:i])
	val = strings.TrimSpace(s[i+1:])
	// Strip a trailing comment, but only when it is clearly one: a `#`
	// inside a quoted value or immediately after a non-space is part of
	// the value (a URL fragment, a shell comment in a command).
	if j := strings.Index(val, " #"); j >= 0 && !strings.HasPrefix(val, "\"") && !strings.HasPrefix(val, "'") {
		val = strings.TrimSpace(val[:j])
	}
	if key == "" {
		return "", "", false
	}
	return key, val, true
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
