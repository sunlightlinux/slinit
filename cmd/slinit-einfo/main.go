// slinit-einfo — OpenRC-compatible einfo(1) multi-applet.
//
// One binary dispatches every applet from OpenRC's libeinfo family
// (einfo, ewarn, eerror, ebegin, eend, veinfo/…, esyslog, ewaitfile,
// eval_ecolors, plus their `n`-suffixed no-newline variants) by
// inspecting basename(argv[0]). Installers ship symlinks whose names
// match the applets scripts already invoke.
//
// The tool is stateless per invocation. Indent / verbose / quiet /
// syslog-tag are all read from the environment (EINFO_INDENT,
// EINFO_VERBOSE, EINFO_QUIET, EINFO_LOG) so init.d shells can adjust
// state via `EINFO_INDENT=$((EINFO_INDENT+2))` and every following
// invocation picks it up.
package main

import (
	"fmt"
	"log/syslog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sunlightlinux/slinit/pkg/einfo"
)

var version = "dev"

// applet describes a single dispatch target: which writer it emits
// to, whether its output waits on EINFO_VERBOSE, whether it prints a
// trailing newline, and which visual level (info/warn/error) it uses.
type applet struct {
	stream   *os.File
	verbose  bool
	newline  bool
	level    einfo.Level
	failCode int // eerror family returns 1 even without --status.
}

// applets covers every OpenRC name we support. The zero-value fields
// pick sensible defaults so entries stay short.
var applets = map[string]applet{
	"einfo":   {stream: os.Stdout, newline: true, level: einfo.LevelInfo},
	"einfon":  {stream: os.Stdout, newline: false, level: einfo.LevelInfo},
	"ewarn":   {stream: os.Stderr, newline: true, level: einfo.LevelWarn},
	"ewarnn":  {stream: os.Stderr, newline: false, level: einfo.LevelWarn},
	"eerror":  {stream: os.Stderr, newline: true, level: einfo.LevelError, failCode: 1},
	"eerrorn": {stream: os.Stderr, newline: false, level: einfo.LevelError, failCode: 1},
	"veinfo":  {stream: os.Stdout, verbose: true, newline: true, level: einfo.LevelInfo},
	"veinfon": {stream: os.Stdout, verbose: true, newline: false, level: einfo.LevelInfo},
	"vewarn":  {stream: os.Stderr, verbose: true, newline: true, level: einfo.LevelWarn},
	"vewarnn": {stream: os.Stderr, verbose: true, newline: false, level: einfo.LevelWarn},
}

func main() {
	name := filepath.Base(os.Args[0])
	// Strip the "slinit-" prefix so operator-facing symlinks work
	// under either the native OpenRC name (einfo) or the slinit
	// namespaced alias (slinit-einfo).
	name = strings.TrimPrefix(name, "slinit-")
	argv := os.Args[1:]
	os.Exit(dispatch(name, argv))
}

func dispatch(name string, argv []string) int {
	switch name {
	case "eval_ecolors":
		fmt.Print(einfo.EvalColors(einfo.ColorsFor(os.Stdout)))
		return 0
	case "eindent", "eoutdent", "veindent", "veoutdent":
		// These would need to mutate EINFO_INDENT in the parent
		// shell, which a subprocess cannot do. The init.d wrappers
		// that expect real indent-tracking manage the variable
		// themselves; the CLI applet is a no-op documented as such.
		return 0
	case "ebegin":
		einfo.Begin(os.Stdout, false, strings.Join(argv, " "))
		return 0
	case "vebegin":
		einfo.Begin(os.Stdout, true, strings.Join(argv, " "))
		return 0
	case "eend", "veend":
		return runEnd(argv, false, name == "veend")
	case "ewend", "vewend":
		return runEnd(argv, true, name == "vewend")
	case "esyslog", "elog":
		return runSyslog(argv)
	case "ewaitfile":
		return runWaitFile(argv)
	}
	if a, ok := applets[name]; ok {
		msg := strings.Join(argv, " ")
		einfo.Emit(a.stream, a.level, a.verbose, a.newline, msg)
		return a.failCode
	}
	fmt.Fprintf(os.Stderr, "slinit-einfo: unknown applet %q\n", name)
	return 1
}

// runEnd parses the leading integer status from argv and hands off to
// einfo.End / EndWarn. warnColour=true selects the ewend palette.
func runEnd(argv []string, warnColour, verbose bool) int {
	if len(argv) == 0 {
		return endMarker(0, "", warnColour, verbose)
	}
	code, err := strconv.Atoi(argv[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "eend: invalid status %q\n", argv[0])
		return 1
	}
	msg := strings.Join(argv[1:], " ")
	return endMarker(code, msg, warnColour, verbose)
}

func endMarker(code int, msg string, warnColour, verbose bool) int {
	// eend goes to stdout so it visually overwrites the earlier
	// ebegin (same stream); ewend does the same.
	if warnColour {
		return einfo.EndWarn(os.Stdout, verbose, code, msg)
	}
	return einfo.End(os.Stdout, verbose, code, msg)
}

// runSyslog implements the `esyslog FACILITY.LEVEL TAG MSG...` and
// `elog FACILITY.LEVEL TAG MSG...` applets (OpenRC einfo.c, logger(1)
// -p order). See parseSyslogPriority for the accepted forms.
func runSyslog(argv []string) int {
	if len(argv) < 3 {
		fmt.Fprintln(os.Stderr, "esyslog: usage: esyslog FACILITY.LEVEL TAG MSG...")
		return 1
	}
	prio, err := parseSyslogPriority(argv[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "esyslog: %v\n", err)
		return 1
	}
	tag := argv[1]
	msg := strings.Join(argv[2:], " ")
	w, err := syslog.New(prio, tag)
	if err != nil {
		// Syslog unreachable (e.g. no /dev/log in a container).
		// Fall back to stderr so the operator still sees the message.
		fmt.Fprintf(os.Stderr, "%s: %s\n", tag, msg)
		return 0
	}
	defer w.Close()
	_, _ = w.Write([]byte(msg))
	return 0
}

// parseSyslogPriority accepts, in order of preference:
//
//   - FACILITY.LEVEL, as OpenRC's esyslog and logger(1) -p take it
//     (`daemon.notice`); LEVEL may also be a number 0-7;
//   - LEVEL.FACILITY, the order slinit used to require (`notice.daemon`),
//     kept so existing scripts keep working — the facility and level
//     name sets are disjoint, so the two orders never collide;
//   - a bare LEVEL name, facility user;
//   - a bare integer, used as the whole priority.
func parseSyslogPriority(spec string) (syslog.Priority, error) {
	// Numeric form.
	if n, err := strconv.Atoi(spec); err == nil {
		return syslog.Priority(n), nil
	}
	i := strings.IndexByte(spec, '.')
	if i < 0 {
		sev, ok := lookupSeverity(spec)
		if !ok {
			return 0, fmt.Errorf("unknown syslog level %q", spec)
		}
		return sev | syslog.LOG_USER, nil
	}
	first, second := spec[:i], spec[i+1:]
	if fac, ok := syslogFacilities[strings.ToLower(first)]; ok {
		sev, ok := lookupSeverity(second)
		if !ok {
			return 0, fmt.Errorf("unknown syslog level %q in %q", second, spec)
		}
		return fac | sev, nil
	}
	if sev, ok := lookupSeverity(first); ok {
		if fac, ok := syslogFacilities[strings.ToLower(second)]; ok {
			return fac | sev, nil
		}
	}
	return 0, fmt.Errorf("invalid syslog priority %q: want FACILITY.LEVEL, e.g. daemon.notice", spec)
}

// lookupSeverity resolves a level name or a number 0-7.
func lookupSeverity(name string) (syslog.Priority, bool) {
	if n, err := strconv.Atoi(name); err == nil {
		if n < 0 || n > 7 {
			return 0, false
		}
		return syslog.Priority(n), true
	}
	sev, ok := syslogSeverities[strings.ToLower(name)]
	return sev, ok
}

var syslogSeverities = map[string]syslog.Priority{
	"emerg":   syslog.LOG_EMERG,
	"panic":   syslog.LOG_EMERG,
	"alert":   syslog.LOG_ALERT,
	"crit":    syslog.LOG_CRIT,
	"err":     syslog.LOG_ERR,
	"error":   syslog.LOG_ERR,
	"warning": syslog.LOG_WARNING,
	"warn":    syslog.LOG_WARNING,
	"notice":  syslog.LOG_NOTICE,
	"info":    syslog.LOG_INFO,
	"debug":   syslog.LOG_DEBUG,
}

var syslogFacilities = map[string]syslog.Priority{
	"kern":     syslog.LOG_KERN,
	"user":     syslog.LOG_USER,
	"mail":     syslog.LOG_MAIL,
	"daemon":   syslog.LOG_DAEMON,
	"auth":     syslog.LOG_AUTH,
	"security": syslog.LOG_AUTH,
	"authpriv": syslog.LOG_AUTHPRIV,
	"ftp":      syslog.LOG_FTP,
	"syslog":   syslog.LOG_SYSLOG,
	"lpr":      syslog.LOG_LPR,
	"news":     syslog.LOG_NEWS,
	"uucp":     syslog.LOG_UUCP,
	"cron":     syslog.LOG_CRON,
	"local0":   syslog.LOG_LOCAL0,
	"local1":   syslog.LOG_LOCAL1,
	"local2":   syslog.LOG_LOCAL2,
	"local3":   syslog.LOG_LOCAL3,
	"local4":   syslog.LOG_LOCAL4,
	"local5":   syslog.LOG_LOCAL5,
	"local6":   syslog.LOG_LOCAL6,
	"local7":   syslog.LOG_LOCAL7,
}

// runWaitFile: `ewaitfile TIMEOUT PATH...` polls every 20ms until
// each path exists or TIMEOUT seconds elapse (TIMEOUT <= 0 = no
// timeout). A begin/end pair frames each wait so operators see
// progress live.
func runWaitFile(argv []string) int {
	if len(argv) < 2 {
		fmt.Fprintln(os.Stderr, "ewaitfile: usage: ewaitfile TIMEOUT PATH [PATH...]")
		return 1
	}
	timeout, err := strconv.Atoi(argv[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "ewaitfile: invalid timeout %q\n", argv[0])
		return 1
	}
	for _, path := range argv[1:] {
		einfo.Begin(os.Stdout, true, fmt.Sprintf("Waiting for %s", path))
		if !waitFor(path, timeout) {
			einfo.EndWarn(os.Stdout, true, 1,
				fmt.Sprintf("timed out waiting for %s", path))
			return 1
		}
		einfo.End(os.Stdout, true, 0, "")
	}
	return 0
}

func waitFor(path string, timeoutSec int) bool {
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		if timeoutSec > 0 && !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}
