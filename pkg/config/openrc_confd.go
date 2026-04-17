// openrc_confd.go wires OpenRC's /etc/rc.conf + /etc/conf.d/<service>
// convention into slinit's init.d auto-detection. OpenRC init scripts
// expect their per-service conf.d file to be sourced (shell syntax)
// before `start`/`stop` runs so they see environment variables like
// `pidfile=...` or `command_args=...`.
//
// Rather than parse those files ourselves (shell quoting is its own
// adventure) we wrap the init.d invocation in `sh -c` and let a real
// shell do the sourcing. That keeps slinit's service-description
// surface unchanged — the wrapped Command is what the ProcessService
// sees, identical to any other scripted service — and matches
// OpenRC's own behaviour of delegating conf.d sourcing to the shell.

package config

import (
	"fmt"
)

// OpenRC-ish paths. Configurable via setters so distributions that
// ship the files elsewhere (or tests that stage them in temp dirs)
// can still exercise the code path.
var (
	openrcRCConf     = "/etc/rc.conf"
	openrcConfDDir   = "/etc/conf.d"
	openrcShellPath  = "/bin/sh"
)

// SetOpenRCPaths overrides the rc.conf path and the conf.d directory.
// Empty strings preserve the current value so callers can set one
// without clobbering the other. Primarily for tests and distro
// packaging where the layout differs from the Gentoo default.
func SetOpenRCPaths(rcConf, confDDir string) {
	if rcConf != "" {
		openrcRCConf = rcConf
	}
	if confDDir != "" {
		openrcConfDDir = confDDir
	}
}

// SetOpenRCShell overrides the shell used to source conf.d files.
// Empty string is ignored.
func SetOpenRCShell(shell string) {
	if shell != "" {
		openrcShellPath = shell
	}
}

// wrapInitdWithConfD returns an argv that sources /etc/rc.conf and
// /etc/conf.d/<name> (if present) before executing the init.d script
// with the requested action. The resulting command is safe to run
// when either file is missing — each source is guarded by a `-r` test
// so a fresh install without rc.conf still works.
//
// Shell-quoting note: scriptPath and name flow from the filesystem, so
// they're trusted to contain valid path characters. They're
// single-quoted with any embedded single quotes escaped via the
// standard `'\''` idiom to be defensive against surprise characters
// (a service named "weird'one" is unlikely but we handle it cleanly).
func wrapInitdWithConfD(scriptPath, name, action string) []string {
	snippet := fmt.Sprintf(
		"[ -r %s ] && . %s; [ -r %s/%s ] && . %s/%s; exec %s %s",
		shellQuote(openrcRCConf), shellQuote(openrcRCConf),
		shellQuote(openrcConfDDir), shellQuote(name),
		shellQuote(openrcConfDDir), shellQuote(name),
		shellQuote(scriptPath), shellQuote(action),
	)
	return []string{openrcShellPath, "-c", snippet}
}

// shellQuote produces a single-quoted POSIX-shell literal for s.
// Embedded single quotes become '\'' which terminates the current
// quoted run, emits a literal quote, then starts a new quoted run.
func shellQuote(s string) string {
	// Hot path: no quotes inside → one pair of quotes around it.
	if !containsRune(s, '\'') {
		return "'" + s + "'"
	}
	out := make([]byte, 0, len(s)+8)
	out = append(out, '\'')
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'', '\\', '\'', '\'')
			continue
		}
		out = append(out, s[i])
	}
	out = append(out, '\'')
	return string(out)
}

func containsRune(s string, r byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == r {
			return true
		}
	}
	return false
}
