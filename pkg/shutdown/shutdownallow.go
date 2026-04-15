package shutdown

import (
	"bufio"
	"os"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/utmp"
)

// DefaultShutdownAllowPaths is the ordered list of paths slinit consults
// for a shutdown.allow file. The first one that exists wins. The slinit
// path is preferred so coexistence with sysvinit on the same machine is
// possible.
var DefaultShutdownAllowPaths = []string{
	"/etc/slinit/shutdown.allow",
	"/etc/shutdown.allow",
}

// listSessionsFunc is the utmp lookup used for access-control checks.
// Overridable so tests do not need a real utmp database.
var listSessionsFunc = utmp.ListUserSessions

// LoadShutdownAllow reads an authorised-users file and returns the list
// of user names it contains. Lines beginning with '#' and blank lines
// are skipped. A non-existent file returns (nil, nil) — callers treat
// that as "access control disabled" rather than an error.
func LoadShutdownAllow(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var users []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Allow trailing comments so operators can document each entry.
		if idx := strings.IndexByte(line, '#'); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
			if line == "" {
				continue
			}
		}
		users = append(users, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return users, nil
}

// FindShutdownAllow returns the first existing shutdown.allow path from
// the candidates list, or an empty string if none exist. The default
// candidate list is DefaultShutdownAllowPaths.
func FindShutdownAllow(candidates []string) string {
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// CheckShutdownAllow decides whether a signal-driven shutdown should
// proceed given the current shutdown.allow configuration.
//
// Semantics (matching sysvinit's shutdown -a):
//
//   - If path is empty or the file does not exist, access control is
//     disabled: the shutdown is allowed unconditionally.
//   - If the file exists but lists no users, it is treated as a lock-out:
//     no signal-driven shutdown is permitted.
//   - Otherwise the shutdown is allowed iff at least one of the listed
//     users is currently logged in (according to utmpx). The rationale
//     is that the person who pressed Ctrl+Alt+Del / sent SIGINT to init
//     must be physically present on a TTY.
//
// The second return value indicates whether the check was actually
// gated. Callers that only care about the allow/deny decision can
// ignore it; it is useful in logs so the operator can tell the
// difference between "allowed (no access control)" and "allowed (user
// matched)".
func CheckShutdownAllow(path string, logger *logging.Logger) (allowed, gated bool) {
	if path == "" {
		return true, false
	}

	// Distinguish "file missing" from "file present but empty". The
	// former disables access control; the latter is a deliberate
	// lock-out and must deny.
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return true, false
		}
		if logger != nil {
			logger.Error("shutdown.allow: cannot stat %s: %v (denying shutdown)", path, err)
		}
		return false, true
	}

	users, err := LoadShutdownAllow(path)
	if err != nil {
		if logger != nil {
			logger.Error("shutdown.allow: cannot read %s: %v (denying shutdown)", path, err)
		}
		return false, true
	}
	if len(users) == 0 {
		if logger != nil {
			logger.Notice("shutdown.allow: %s is empty — signal-driven shutdown denied", path)
		}
		return false, true
	}

	sessions := listSessionsFunc()
	if len(sessions) == 0 {
		if logger != nil {
			logger.Notice("shutdown.allow: no logged-in users — signal-driven shutdown denied")
		}
		return false, true
	}

	// Build a small set so a machine with many sessions doesn't do an
	// O(N*M) scan (N=allow list, M=sessions).
	allowed = false
	allowSet := make(map[string]struct{}, len(users))
	for _, u := range users {
		allowSet[u] = struct{}{}
	}
	var matchedUser, matchedLine string
	for _, s := range sessions {
		if _, ok := allowSet[s.User]; ok {
			allowed = true
			matchedUser = s.User
			matchedLine = s.Line
			break
		}
	}
	if !allowed {
		if logger != nil {
			logger.Notice("shutdown.allow: no authorised user is logged in — signal-driven shutdown denied")
		}
		return false, true
	}
	if logger != nil {
		logger.Notice("shutdown.allow: authorised user %q on %s — shutdown permitted",
			matchedUser, matchedLine)
	}
	return true, true
}
