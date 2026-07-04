// openrc_depend.go extracts the OpenRC-style `depend()` block from an
// init.d script. Unlike LSB scripts — whose deps live in the
// `### BEGIN INIT INFO ###` comment header — OpenRC scripts declare
// dependencies inside a shell function that may run arbitrary logic
// (see `netmount` which shells out to `fstabinfo` mid-body).
//
// A regex scan would miss those conditional branches, so we execute
// the function in a sandbox: `sh -c` sources the script with every
// depend-directive (need/use/want/after/before/provide/keyword)
// rebound to a line-emitter, plus every openrc-run helper (ebegin,
// eerror, etc.) stubbed to a no-op. The resulting stdout is
// deterministic even for scripts that would print status text at
// source-time.
package config

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// OpenRCDepend is the parsed shape of a `depend()` function.
type OpenRCDepend struct {
	Need    []string // hard deps
	Use     []string // soft: start if present, don't fail if absent
	Want    []string // soft: same effect, modern spelling
	After   []string // ordering only, no dep
	Before  []string // ordering only, no dep
	Provide []string // virtual names we advertise
	Keyword []string // contexts we opt out of ("-docker", "-lxc", …)
}

// HasAny reports whether the parsed block yielded any directive at
// all. Used by the initd loader to decide between LSB and OpenRC
// parse results when both might be present.
func (d *OpenRCDepend) HasAny() bool {
	if d == nil {
		return false
	}
	return len(d.Need)+len(d.Use)+len(d.Want)+
		len(d.After)+len(d.Before)+len(d.Provide)+len(d.Keyword) > 0
}

// The sandbox script wraps the target init.d script with:
//   - directive rebinds that emit one "KIND ARG" line per argument
//   - openrc-run helpers rebound to no-ops (so any accidental call at
//     source-time from top-level shell code stays silent)
//   - the actual invocation of depend() with stdout captured
//
// Argument shell-safety: the target path is single-quoted after any
// literal quotes inside are escaped by the caller. sh's dot builtin
// tolerates the script's `#!/sbin/openrc-run` shebang because the
// line is treated as a comment.
const openrcDependSandbox = `
_emit() {
  kind=$1
  shift
  for x in "$@"; do
    printf '%s %s\n' "$kind" "$x"
  done
}
need()    { _emit need    "$@"; }
use()     { _emit use     "$@"; }
want()    { _emit want    "$@"; }
after()   { _emit after   "$@"; }
before()  { _emit before  "$@"; }
provide() { _emit provide "$@"; }
keyword() { _emit keyword "$@"; }

# Neutralise openrc-run helpers that a script might invoke at
# source-time (top-level assignments occasionally call einfo etc.).
ebegin()  { :; }
eend()    { :; }
einfo()   { :; }
einfon()  { :; }
ewarn()   { :; }
ewarnn()  { :; }
eerror()  { :; }
eerrorn() { :; }
veinfo()  { :; }
vewarn()  { :; }
eindent() { :; }
eoutdent(){ :; }
yesno()   { return 1; }

# Some scripts read fstabinfo/mountinfo mid-depend() to decide
# conditional deps. Stub them so the parse doesn't hang on a missing
# binary or emit noise.
fstabinfo()  { :; }
mountinfo()  { :; }

# Silence unbound variable references common in OpenRC scripts.
: "${RC_SVCNAME:=}"
: "${RC_LIBEXECDIR:=}"
: "${RC_CACHEDIR:=}"
: "${RC_SYS:=}"
: "${RC_DEFAULTLEVEL:=default}"
: "${RC_BOOTLEVEL:=boot}"

# Absorb the script; anything not a function definition still runs
# but our stubs above absorb the side effects.
. "$1" 2>/dev/null || :

# If the script does not define depend(), our call is a no-op. The
# trailing ':' guarantees a zero exit so the caller does not treat a
# depend-less script as a parse failure.
type depend >/dev/null 2>&1 && depend 2>/dev/null
:
`

// dependParseTimeout caps the sandbox at a small budget. Real scripts
// return within a few milliseconds; anything longer is almost always
// an accidental infinite loop and would stall the loader otherwise.
const dependParseTimeout = 2 * time.Second

// ParseOpenRCDepend executes the sandbox with scriptPath as the
// script under analysis. Errors from the shell itself are non-fatal
// as long as at least one directive line was emitted — partial parses
// are more useful than none for a best-effort init.d compat path.
func ParseOpenRCDepend(scriptPath string) (*OpenRCDepend, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dependParseTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", openrcDependSandbox, "sandbox", scriptPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Detach from any env vars the parent has bound so RC_SVCNAME
	// etc. seen by the script are only the defaults we set inside
	// the sandbox.
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"}

	err := cmd.Run()
	// A non-zero exit is fine if the script errored after emitting
	// some lines; report the error only when there's nothing to
	// return.
	dep := parseDependOutput(stdout.String())
	if err != nil && !dep.HasAny() {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("openrc depend() parse timed out after %s: %s",
				dependParseTimeout, strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("openrc depend() parse: %w: %s", err,
			strings.TrimSpace(stderr.String()))
	}
	return dep, nil
}

// parseDependOutput turns "KIND ARG\n" lines into an OpenRCDepend.
// Order-preserving with per-kind dedup so a script that calls
// `after clock` twice yields "clock" once.
func parseDependOutput(text string) *OpenRCDepend {
	dep := &OpenRCDepend{}
	seen := map[string]struct{}{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		space := strings.IndexByte(line, ' ')
		if space <= 0 {
			continue
		}
		kind := line[:space]
		arg := strings.TrimSpace(line[space+1:])
		if arg == "" {
			continue
		}
		key := kind + "\x00" + arg
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		switch kind {
		case "need":
			dep.Need = append(dep.Need, arg)
		case "use":
			dep.Use = append(dep.Use, arg)
		case "want":
			dep.Want = append(dep.Want, arg)
		case "after":
			dep.After = append(dep.After, arg)
		case "before":
			dep.Before = append(dep.Before, arg)
		case "provide":
			dep.Provide = append(dep.Provide, arg)
		case "keyword":
			dep.Keyword = append(dep.Keyword, arg)
		}
	}
	return dep
}

// LooksLikeOpenRCScript returns true when the first shebang line
// names openrc-run (the OpenRC script interpreter). Used by the
// initd loader to skip the OpenRC parse for plain sysvinit / LSB
// scripts that couldn't possibly define depend().
func LooksLikeOpenRCScript(header string) bool {
	// Only the first line matters. Trim whitespace after `#!` so
	// `#!/sbin/openrc-run` and `#! /sbin/openrc-run` both match.
	if !strings.HasPrefix(header, "#!") {
		return false
	}
	line := header[2:]
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	line = strings.TrimSpace(line)
	return strings.HasSuffix(line, "/openrc-run") ||
		strings.HasSuffix(line, "openrc-run")
}
