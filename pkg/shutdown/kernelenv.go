package shutdown

import (
	"fmt"
	"os"
	"strings"
)

// extractKernelEnvStore parses /proc/cmdline for KEY=VALUE tokens and
// writes them to dest as an env-file (one KEY=VALUE per line, LF
// terminated, no shell quoting). Bare-word flags (`quiet`, `ro`) are
// skipped: only tokens whose first '=' produces a valid environment
// variable name are emitted, so a stray `debug` or `nosplash` on the
// cmdline never leaks into the extracted store.
//
// The file is written atomically (temp + rename) with 0444 permissions
// so services that read it in their sandbox can't accidentally corrupt
// it via a stray write, and no writer races with a partially-written
// file at boot time.
//
// Returns (number of entries written, error). A missing /proc/cmdline
// (test / unusual embedded environment) is reported as an error; an
// empty cmdline succeeds with 0 entries.
func extractKernelEnvStore(dest string) (int, error) {
	raw, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return 0, fmt.Errorf("read /proc/cmdline: %w", err)
	}
	entries := parseKernelEnvTokens(string(raw))

	if err := os.MkdirAll(parentDir(dest), 0755); err != nil {
		return 0, fmt.Errorf("mkdir %s: %w", parentDir(dest), err)
	}

	// Atomic write via temp + rename so a reader never sees a
	// half-populated file.
	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0444)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", tmp, err)
	}
	for _, e := range entries {
		if _, werr := f.WriteString(e + "\n"); werr != nil {
			f.Close()
			os.Remove(tmp)
			return 0, fmt.Errorf("write %s: %w", tmp, werr)
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return 0, fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return 0, fmt.Errorf("rename %s -> %s: %w", tmp, dest, err)
	}
	return len(entries), nil
}

// parseKernelEnvTokens extracts KEY=VALUE tokens from a whitespace-
// separated kernel command line, returning them as "KEY=VALUE"
// strings ready to write into an env-file. Split out so tests can
// exercise the token grammar without touching /proc.
//
// Grammar (matching what the kernel treats as environment):
//   - Split on any whitespace.
//   - A token must contain '='; the key is everything before the
//     first '=' and the value is everything after.
//   - Key must be non-empty and match [A-Z_][A-Z0-9_]* (case-
//     insensitive) — that's stricter than POSIX sh env, but relaxed
//     enough to accept the common kernel keys (console=, root=,
//     rd.lvm.lv=, resume=UUID=…). A key containing '.', '-' or other
//     env-invalid bytes is silently dropped: env-file consumers
//     wouldn't be able to source it anyway.
//   - Values are taken verbatim, including embedded '=' (e.g.
//     `resume=UUID=abc-123` → key `resume`, value `UUID=abc-123`).
func parseKernelEnvTokens(cmdline string) []string {
	var out []string
	for _, tok := range strings.Fields(cmdline) {
		i := strings.IndexByte(tok, '=')
		if i <= 0 {
			// No '=' or leading '=' — bare flag or malformed.
			continue
		}
		key := tok[:i]
		if !isEnvKey(key) {
			continue
		}
		out = append(out, tok)
	}
	return out
}

// isEnvKey reports whether s is a valid POSIX-ish shell environment
// variable name: starts with letter or '_', continues with letters,
// digits, or '_'. Case-insensitive on the leading letter to accept the
// lower-case kernel keys typical on Linux boots (console, root, quiet).
func isEnvKey(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_':
			continue
		case r >= 'A' && r <= 'Z':
			continue
		case r >= 'a' && r <= 'z':
			continue
		case r >= '0' && r <= '9' && i > 0:
			continue
		default:
			return false
		}
	}
	return true
}
