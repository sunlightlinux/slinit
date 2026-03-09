package config

import (
	"fmt"
	"unicode/utf8"
)

// MaxDepDepth limits the depth of the dependency tree to prevent stack
// exhaustion from deeply recursive service references (e.g. svc@1 -> svc@2 -> ...).
const MaxDepDepth = 32

// ValidateServiceName checks that a service name is well-formed.
// Rules (matching dinit):
//   - Must not be empty
//   - Must not start with '.' or '@'
//   - Characters before '@' must be alphanumeric, '.', '_', '-', or UTF-8 >= 128
func ValidateServiceName(name string) error {
	if name == "" {
		return fmt.Errorf("service name is empty")
	}
	if name[0] == '.' {
		return fmt.Errorf("service name must not start with '.'")
	}
	if name[0] == '@' {
		return fmt.Errorf("service name must not start with '@'")
	}

	for i := 0; i < len(name); {
		r, size := utf8.DecodeRuneInString(name[i:])
		if r == '@' {
			break // anything after '@' is allowed (service argument)
		}
		if r >= 128 {
			// UTF-8 multi-byte characters are allowed
			i += size
			continue
		}
		ch := name[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') ||
			ch == '.' || ch == '_' || ch == '-' || ch == '/' {
			i++
			continue
		}
		return fmt.Errorf("service name contains invalid character: %q", string(ch))
	}

	return nil
}
