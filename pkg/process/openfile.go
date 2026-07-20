package process

import (
	"fmt"
	"os"
	"strings"
)

// openConfiguredFile opens the path in e per the systemd OpenFile=
// semantics. Options is a comma-separated list; recognised tokens:
//
//   read-only   — O_RDONLY (default is O_RDWR|O_CREATE).
//   append      — O_APPEND (with default write flags).
//   truncate    — O_TRUNC on open (with default write flags).
//   graceful    — treat "open failed" as non-fatal: the parent
//                 substitutes /dev/null so ExtraFiles keeps the fd
//                 slot stable. Systemd's exact semantics.
//
// Unknown tokens are refused so a typo surfaces at start time rather
// than silently downgrading protection.
func openConfiguredFile(e OpenFileEntry) (*os.File, error) {
	flags := os.O_RDWR | os.O_CREATE
	graceful := false
	for _, tok := range splitOpenFileOptions(e.Options) {
		switch tok {
		case "":
			continue
		case "read-only":
			flags = os.O_RDONLY
		case "append":
			flags = (flags &^ (os.O_RDWR | os.O_RDONLY)) | os.O_WRONLY | os.O_APPEND | os.O_CREATE
		case "truncate":
			flags = (flags &^ os.O_APPEND) | os.O_TRUNC
		case "graceful":
			graceful = true
		default:
			return nil, fmt.Errorf("unknown option %q (accepted: read-only, append, truncate, graceful)", tok)
		}
	}
	f, err := os.OpenFile(e.Path, flags, 0o644)
	if err != nil {
		if graceful {
			return os.Open("/dev/null")
		}
		return nil, err
	}
	return f, nil
}

func splitOpenFileOptions(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}
