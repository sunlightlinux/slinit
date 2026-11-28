package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// The execution-environment directives of a .service unit: stdio,
// credentials, the environment filters, the TTY cluster, utmp, the
// per-service log filters, and the ExecStopPost= / ExecReload= lines.
//
// Most of these are custom rules rather than key/conv pairs, for one of
// three reasons. systemd expands specifiers in their values (%n, %t),
// and the expansion needs the unit's name, which a conv does not see.
// An empty assignment resets them, where a conv can only emit nothing.
// Or what they map to depends on another directive of the unit, which
// may come before or after it, in a drop-in as well: StandardError=null
// cannot be honoured once stdout goes to a logfile, a SetCredential= is
// only a fallback for a LoadCredential= of the same name, ExecReload=
// and ReloadSignal= name one signal between them, a finish-command
// needs type = process. Such a rule records what the line says in
// exState, and exFinish decides once the whole unit is read, so the
// order of the lines does not change the result, as in systemd.

// exState is what the execution-environment rules saw, for exFinish.
type exState struct {
	in, out, err exStream
	data         []byte // StandardInputText= / StandardInputData=, as systemd buffers them
	ttyPath      string // TTYPath=, "" when unset
	utmpID       string

	load, set []exCred
	imports   []string

	reloadSignal, reloadSignalRaw string // ReloadSignal=, in slinit's spelling
	execReloadSignal              string // what ExecReload=kill -SIG $MAINPID sends
	execReloadLine                string
	execReloadWarns               []SystemdWarning
}

func exStateOf(cfg *SystemdConfig) *exState {
	return sdState(cfg, "exec", func() *exState { return &exState{} })
}

// exFinish settles the mappings that depend on more than one line.
func exFinish(cfg *SystemdConfig) []SystemdWarning {
	st := exStateOf(cfg)
	warns := exFinishStdio(cfg, st)
	warns = append(warns, exFinishCreds(cfg, st)...)
	warns = append(warns, exFinishReload(cfg, st)...)
	return append(warns, exFinishStopPost(cfg)...)
}

func init() {
	// The TTY cluster. slinit acts on tty-path only, where systemd acts
	// on TTYPath= (or /dev/console) whenever the service touches a TTY.
	registerService("TTYPath", sdRule{custom: mapTTYPath})
	registerService("TTYReset", sdRule{key: "tty-reset", conv: ttyBoolConv("TTYReset",
		"systemd resets the terminal before and after the service runs; slinit before only")})
	registerService("TTYVHangup", sdRule{key: "tty-vhangup", conv: ttyBoolConv("TTYVHangup",
		"systemd hangs the terminal up before and after the service runs; slinit before only")})
	registerService("TTYVTDisallocate", sdRule{key: "tty-vt-disallocate", conv: ttyBoolConv("TTYVTDisallocate",
		"systemd deallocates the VT before and after the service runs, and clears a non-VT terminal with ANSI sequences; "+
			"slinit deallocates a VT before only, and leaves other terminals alone")})
	registerService("TTYRows", exScalar("tty-rows", ttySizeConv))
	registerService("TTYColumns", exScalar("tty-columns", ttySizeConv))

	registerService("UtmpIdentifier", sdRule{custom: mapUtmpIdentifier})
	registerService("UtmpMode", sdRule{key: "utmp-mode", conv: utmpModeConv})

	registerService("TimerSlackNSec", sdRule{key: "timer-slack-nsec", conv: timerSlackConv})
	registerService("ExecSearchPath", sdRule{custom: mapExecSearchPath})

	// The per-service log filters. slinit applies them to the lines a
	// type = process service writes to its logfile; systemd to what the
	// journal receives from the unit.
	registerService("LogLevelMax", sdRule{key: "log-level-max", conv: logLevelMaxConv})
	registerService("LogRateLimitIntervalSec", sdRule{custom: mapLogRateLimit})
	registerService("LogRateLimitBurst", sdRule{custom: mapLogRateLimit})
	registerService("LogFilterPatterns", sdRule{custom: mapLogFilterPatterns})

	registerService("StandardInput", sdRule{custom: mapStandardInput})
	registerService("StandardOutput", sdRule{custom: mapStandardOutput})
	registerService("StandardError", sdRule{custom: mapStandardError})
	registerService("StandardInputText", sdRule{custom: mapStandardInputData})
	registerService("StandardInputData", sdRule{custom: mapStandardInputData})
	registerService("OpenFile", sdRule{custom: mapOpenFile})

	registerService("LoadCredential", sdRule{custom: mapLoadCredential})
	registerService("SetCredential", sdRule{custom: mapSetCredential})
	registerService("ImportCredential", sdRule{custom: mapImportCredential})

	registerService("UnsetEnvironment", sdRule{custom: mapUnsetEnvironment})

	registerService("ExecStopPost", sdRule{custom: mapExecStopPost})
	registerService("ExecReload", sdRule{custom: mapExecReload})
	registerService("ReloadSignal", sdRule{custom: mapReloadSignal})

	registerFinisher(exFinish)

	sdServiceNoEquivalent["LoadCredentialEncrypted"] = sdGap{why: "slinit cannot decrypt a credential sealed with the TPM2 or host key; " +
		"decrypt it with systemd-creds and use load-credential"}
	sdServiceNoEquivalent["SetCredentialEncrypted"] = sdGap{why: "slinit cannot decrypt a credential sealed with the TPM2 or host key; " +
		"decrypt it with systemd-creds and use set-credential"}
	sdServiceNoEquivalent["LogNamespace"] = sdGap{why: "slinit has no per-service journal namespace (slinit-journald --namespace serves a whole daemon); " +
		"the service's lines go to the default journal"}
	sdServiceNoEquivalent["SyslogFacility"] = sdGap{why: "slinit's journal records carry no facility; " +
		"log-forward-facility sets one for UDP forwarding only"}
	// The bespoke case in applyServiceKey still answers PassEnvironment=
	// with a vaguer note; this entry is reached once that case is gone.
	sdServiceNoEquivalent["PassEnvironment"] = sdGap{why: "slinit's pass-environment also filters the env-file and slinitctl setenv " +
		"variables, which systemd always passes, so mapping it would drop the unit's EnvironmentFile= variables; " +
		"the service inherits slinit's whole environment instead"}
}

// --- shared helpers -------------------------------------------------

// exIgnored is the warning for a value systemd itself would log and
// ignore, or one slinit cannot express: the assignment is dropped.
func exIgnored(key, val string, err error) SystemdWarning {
	return SystemdWarning{"WARN", fmt.Sprintf("[Service] %s=%s ignored: %v", key, val, err)}
}

func exNote(key, val, msg string) SystemdWarning {
	return SystemdWarning{"NOTE", fmt.Sprintf("%s=%s: %s", key, val, msg)}
}

func exWarn(key, val, msg string) SystemdWarning {
	return SystemdWarning{"WARN", fmt.Sprintf("%s=%s: %s", key, val, msg)}
}

// exMapped returns the value of the first assignment of key.
func exMapped(cfg *SystemdConfig, key string) (string, bool) {
	for _, kv := range cfg.mapped {
		if kv.key == key {
			return kv.value, true
		}
	}
	return "", false
}

// exScalar is a scalar rule whose empty assignment resets the setting,
// as systemd's does for the directive; sdRule's own scalar shape can
// only leave an earlier assignment in place there.
func exScalar(key string, conv func(v string) (string, []SystemdWarning, error)) sdRule {
	return sdRule{custom: func(cfg *SystemdConfig, sdKey, val string) []SystemdWarning {
		v := strings.TrimSpace(val)
		if v == "" {
			cfg.dropMapped(key)
			return nil
		}
		out, warns, err := conv(v)
		if err == nil {
			err = validateSlinitSetting(key, out)
		}
		if err != nil {
			return append(warns, exIgnored(sdKey, val, err))
		}
		cfg.setMapped(key, out)
		return warns
	}}
}

// exBool is systemd's parse_boolean, written as slinit's yes/no:
// slinit's parseBool knows neither on/off nor y/t/n/f.
func exBool(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "yes", "y", "true", "t", "on":
		return "yes", nil
	case "0", "no", "n", "false", "f", "off":
		return "no", nil
	}
	return "", fmt.Errorf("%q is not a boolean", v)
}

// exPath expands the specifiers in a path-valued directive and checks
// it the way path_simplify_and_warn(PATH_CHECK_ABSOLUTE) does: absolute,
// with no ".." component. The path is simplified as systemd simplifies
// it (repeated slashes, "." components and a trailing slash go).
func exPath(cfg *SystemdConfig, key, p string) (string, []SystemdWarning, error) {
	p, warns := expandSpecifiers(cfg, key, p)
	if !strings.HasPrefix(p, "/") {
		return "", warns, fmt.Errorf("%q is not an absolute path", p)
	}
	for _, c := range strings.Split(p, "/") {
		if c == ".." {
			return "", warns, fmt.Errorf("%q has a \"..\" component", p)
		}
	}
	return filepath.Clean(p), warns, nil
}

// exLineSafe reports why s cannot be the whole of a slinit value, or
// "": the value is one line, and the parser trims blanks at either end.
func exLineSafe(s string) string {
	switch {
	case strings.ContainsAny(s, "\n\r\x00"):
		return "it holds a newline, carriage return or NUL, which a slinit value of one line cannot"
	case strings.TrimSpace(s) != s:
		return "it starts or ends with a blank, which slinit trims from a value"
	}
	return ""
}

// errSDNul: a C escape for NUL, which systemd accepts in a few values
// and slinit can express in none.
var errSDNul = errors.New("it holds a NUL byte, which slinit cannot express")

// exCUnescape is systemd's cunescape (src/basic/escape.c) without
// UNESCAPE_RELAX: every backslash takes one C escape, and an unknown
// escape or a trailing backslash fails the whole value. An escape for
// NUL fails with errSDNul.
func exCUnescape(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i == len(s) {
			return "", errors.New("trailing backslash")
		}
		out, n, ok := cunescapeOne(s[i:])
		if !ok {
			if isNulEscape(s[i:]) {
				return "", errSDNul
			}
			return "", fmt.Errorf("unknown escape sequence \\%s", s[i:min(i+4, len(s))])
		}
		b.Write(out)
		i += n - 1
	}
	return b.String(), nil
}

func isNulEscape(s string) bool {
	for _, z := range []string{"x00", "000", "u0000", "U00000000"} {
		if strings.HasPrefix(s, z) {
			return true
		}
	}
	return false
}

// exColonWords splits s at ':' as extract_first_word with ":" and
// EXTRACT_DONT_COALESCE_SEPARATORS does, without EXTRACT_CUNESCAPE: a
// backslash takes the next character as it is (a "\:" is a colon inside
// a word), quotes are plain characters, and two colons in a row hold
// an empty word. A trailing backslash is an error.
func exColonWords(s string) ([]string, error) {
	var words []string
	for s != "" {
		w, rest, err := exColonWord(s)
		if err != nil {
			return words, err
		}
		words = append(words, w)
		s = rest
	}
	return words, nil
}

// exColonWord is one step of exColonWords: the word up to the first
// unescaped ':' and what follows that colon.
func exColonWord(s string) (word, rest string, err error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
			if i == len(s) {
				return "", "", errors.New("trailing backslash")
			}
			b.WriteByte(s[i])
		case ':':
			return b.String(), s[i+1:], nil
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String(), "", nil
}

// --- TTY ------------------------------------------------------------

// mapTTYPath records TTYPath=; exFinishStdio sets tty-path from it. An
// empty assignment restores systemd's default, /dev/console.
func mapTTYPath(cfg *SystemdConfig, key, val string) []SystemdWarning {
	st := exStateOf(cfg)
	v := strings.TrimSpace(val)
	if v == "" {
		st.ttyPath = ""
		return nil
	}
	p, warns, err := exPath(cfg, key, v)
	if err == nil {
		err = validateSlinitSetting("tty-path", p)
	}
	if err != nil {
		return append(warns, exIgnored(key, val, err))
	}
	st.ttyPath = p
	return warns
}

// ttyBoolConv is the conv of a TTY switch: a systemd boolean, with a
// note on what slinit does differently when it is on.
func ttyBoolConv(key, diff string) func(string) (string, []SystemdWarning, error) {
	return func(v string) (string, []SystemdWarning, error) {
		b, err := exBool(v)
		if err != nil {
			return "", nil, err
		}
		if b == "no" {
			return b, nil, nil
		}
		return b, []SystemdWarning{exNote(key, v, diff+"; slinit acts only on tty-path (TTYPath=, or StandardInput=tty)")}, nil
	}
}

// ttySizeConv: TTYRows= / TTYColumns=. systemd fills an axis left unset
// from the terminal's current size; slinit sets the size only when it
// has both.
func ttySizeConv(v string) (string, []SystemdWarning, error) {
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil {
		return "", nil, fmt.Errorf("%q is not an unsigned number", v)
	}
	if n > 65535 {
		return "", nil, fmt.Errorf("%d does not fit the terminal's 16-bit size", n)
	}
	return strconv.FormatUint(n, 10), []SystemdWarning{{"NOTE",
		"slinit sets the terminal size only when both TTYRows= and TTYColumns= are given"}}, nil
}

// --- utmp -----------------------------------------------------------

// mapUtmpIdentifier: UtmpIdentifier= → inittab-id, and inittab-line
// from the TTY (exFinishStdio), as systemd fills ut_line from TTYPath=
// minus "/dev/". ut_id holds four bytes: systemd keeps the last four of
// a longer identifier (copy_suffix), where slinit would keep the first
// four.
func mapUtmpIdentifier(cfg *SystemdConfig, key, val string) []SystemdWarning {
	st := exStateOf(cfg)
	v := strings.TrimSpace(val)
	if v == "" {
		st.utmpID = ""
		return nil
	}
	id, warns := expandSpecifiers(cfg, key, v)
	if len(id) > 4 {
		id = id[len(id)-4:]
	}
	if why := exLineSafe(id); why != "" || strings.ContainsFunc(id, unicode.IsControl) {
		if why == "" {
			why = "it holds a control character"
		}
		return append(warns, exIgnored(key, val, errors.New(why)))
	}
	st.utmpID = id
	return warns
}

func utmpModeConv(v string) (string, []SystemdWarning, error) {
	switch v {
	case "init":
		return v, nil, nil
	case "login", "user":
		return v, []SystemdWarning{{"NOTE", "systemd writes an INIT_PROCESS record first and every record to wtmp too, " +
			"and names the user in a USER_PROCESS one; slinit writes the one record, to utmp only, with no user name"}}, nil
	}
	return "", nil, fmt.Errorf("%q is not init, login or user", v)
}

// --- timer slack, search path ---------------------------------------

// exNsecUnits is extract_nsec_multiplier's table (src/basic/time-util.c),
// in its order: a suffix matches as a prefix and the first hit wins. A
// bare number is nanoseconds.
var exNsecUnits = []struct {
	suffix string
	nsec   uint64
}{
	{"seconds", 1e9}, {"second", 1e9}, {"sec", 1e9}, {"s", 1e9},
	{"minutes", 60e9}, {"minute", 60e9}, {"min", 60e9},
	{"months", 2629800e9}, {"month", 2629800e9}, {"M", 2629800e9},
	{"msec", 1e6}, {"ms", 1e6},
	{"m", 60e9},
	{"hours", 3600e9}, {"hour", 3600e9}, {"hr", 3600e9}, {"h", 3600e9},
	{"days", 86400e9}, {"day", 86400e9}, {"d", 86400e9},
	{"weeks", 604800e9}, {"week", 604800e9}, {"w", 604800e9},
	{"years", 31557600e9}, {"year", 31557600e9}, {"y", 31557600e9},
	{"usec", 1e3}, {"us", 1e3}, {"μs", 1e3}, {"µs", 1e3},
	{"nsec", 1}, {"ns", 1},
}

// parseSystemdNsec is systemd's parse_nsec: a list of numbers, each with
// an optional fraction and unit, "1.5ms" or "1s 500us". inf reports
// "infinity".
func parseSystemdNsec(v string) (nsec uint64, inf bool, err error) {
	p := strings.TrimLeft(v, " \t\n\r")
	if rest, ok := strings.CutPrefix(p, "infinity"); ok {
		if strings.TrimSpace(rest) != "" {
			return 0, false, fmt.Errorf("%q is not a time span", v)
		}
		return 0, true, nil
	}
	something := false
	for {
		p = strings.TrimLeft(p, " \t\n\r")
		if p == "" {
			break
		}
		i := 0
		for i < len(p) && p[i] >= '0' && p[i] <= '9' {
			i++
		}
		if i == 0 {
			return 0, false, fmt.Errorf("%q is not a time span", v)
		}
		whole, perr := strconv.ParseUint(p[:i], 10, 64)
		if perr != nil {
			return 0, false, fmt.Errorf("%q is out of range", v)
		}
		p = p[i:]
		frac := ""
		if strings.HasPrefix(p, ".") {
			j := 1
			for j < len(p) && p[j] >= '0' && p[j] <= '9' {
				j++
			}
			frac, p = p[1:j], p[j:]
		}
		mult := uint64(1)
		rest := strings.TrimLeft(p, " \t\n\r")
		matched := false
		for _, u := range exNsecUnits {
			if strings.HasPrefix(rest, u.suffix) {
				mult, p, matched = u.nsec, rest[len(u.suffix):], true
				break
			}
		}
		if !matched && p != "" && p == rest {
			// The number runs straight into something that is no unit:
			// "12.34.56" or "5x". After a blank, the next number starts.
			return 0, false, fmt.Errorf("%q is not a time span", v)
		}
		if whole > (^uint64(0)-nsec)/mult {
			return 0, false, fmt.Errorf("%q is out of range", v)
		}
		nsec += whole * mult
		for k, m := 0, mult/10; k < len(frac) && m > 0; k, m = k+1, m/10 {
			nsec += uint64(frac[k]-'0') * m
		}
		something = true
	}
	if !something {
		return 0, false, fmt.Errorf("%q is not a time span", v)
	}
	return nsec, false, nil
}

// timerSlackConv: TimerSlackNSec= → timer-slack-nsec, in plain
// nanoseconds. infinity leaves the slack alone in systemd, which is what
// slinit's 0 does; and 0 in systemd resets it to the default, as
// PR_SET_TIMERSLACK with 0 does in slinit.
func timerSlackConv(v string) (string, []SystemdWarning, error) {
	n, inf, err := parseSystemdNsec(v)
	if err != nil {
		return "", nil, err
	}
	if inf {
		return "0", nil, nil
	}
	return strconv.FormatUint(n, 10), nil, nil
}

// mapExecSearchPath: ExecSearchPath= → exec-search-path. systemd takes a
// colon-separated list, each assignment adding to it and an empty one
// clearing it; slinit takes the PATH value whole, so the list is kept
// joined. A path that is not absolute ends the line, as in systemd,
// which keeps the paths before it.
func mapExecSearchPath(cfg *SystemdConfig, key, val string) []SystemdWarning {
	v := strings.TrimSpace(val)
	if v == "" {
		cfg.dropMapped("exec-search-path")
		return nil
	}
	words, werr := exColonWords(v)
	var warns []SystemdWarning
	var paths []string
	for _, w := range words {
		p, pw, err := exPath(cfg, key, w)
		warns = append(warns, pw...)
		if err == nil && strings.Contains(p, ":") {
			err = fmt.Errorf("%q holds a colon, which a PATH entry cannot", p)
		}
		if err != nil {
			warns = append(warns, exIgnored(key, val, fmt.Errorf("%v; the paths after it are left out", err)))
			werr = nil
			break
		}
		paths = append(paths, literalDollars(p))
	}
	if werr != nil {
		warns = append(warns, exIgnored(key, val, fmt.Errorf("%v; the paths after it are left out", werr)))
	}
	if len(paths) == 0 {
		return warns
	}
	joined := strings.Join(paths, ":")
	if cur, ok := exMapped(cfg, "exec-search-path"); ok {
		joined = cur + ":" + joined
	}
	cfg.setMapped("exec-search-path", joined)
	return append(warns, exNote(key, val, "→ exec-search-path, the PATH the service gets, even over a PATH its EnvironmentFile= sets "+
		"(systemd keeps that one); a command named without a path in ExecStart= is not reliably looked up through it — write its full path"))
}

// --- log filters ----------------------------------------------------

const exLogfileOnly = "slinit applies it to the lines a type = process service writes to its logfile (StandardOutput=file:/append:); " +
	"systemd to what the journal gets from the unit"

// logLevelMaxConv: LogLevelMax= → log-level-max. systemd takes exactly
// the level names and 0..7 (log_level_from_string); slinit's parser
// takes more, so the value is checked here, where systemd checks it.
func logLevelMaxConv(v string) (string, []SystemdWarning, error) {
	switch v {
	case "emerg", "alert", "crit", "err", "warning", "notice", "info", "debug",
		"0", "1", "2", "3", "4", "5", "6", "7":
		return v, []SystemdWarning{{"NOTE", exLogfileOnly + "; slinit also reads the level from a leading ERROR:/WARN:-style keyword, " +
			"where systemd takes such a line as info"}}, nil
	}
	return "", nil, fmt.Errorf("%q is not a log level", v)
}

// journald's defaults (journald.conf RateLimitIntervalSec=, RateLimitBurst=):
// systemd uses them for whichever of the pair the unit leaves out, where
// slinit limits nothing unless it has both.
const (
	exRateIntervalDefault = "30s"
	exRateBurstDefault    = "10000"
)

// mapLogRateLimit: LogRateLimitIntervalSec= / LogRateLimitBurst= →
// log-rate-limit-interval / -burst. 0 turns the limit off in both.
func mapLogRateLimit(cfg *SystemdConfig, key, val string) []SystemdWarning {
	v := strings.TrimSpace(val)
	var out, slinitKey, otherKey, otherDefault string
	if key == "LogRateLimitIntervalSec" {
		slinitKey, otherKey, otherDefault = "log-rate-limit-interval", "log-rate-limit-burst", exRateBurstDefault
		secs, ok := parseSystemdSeconds(v, "")
		if !ok {
			return []SystemdWarning{exIgnored(key, val, fmt.Errorf("%q is not a finite time span", v))}
		}
		out = secs + "s"
	} else {
		slinitKey, otherKey, otherDefault = "log-rate-limit-burst", "log-rate-limit-interval", exRateIntervalDefault
		n, err := strconv.ParseUint(v, 10, 31)
		if err != nil {
			return []SystemdWarning{exIgnored(key, val, fmt.Errorf("%q is not an unsigned number", v))}
		}
		out = strconv.FormatUint(n, 10)
	}
	if err := validateSlinitSetting(slinitKey, out); err != nil {
		return []SystemdWarning{exIgnored(key, val, err)}
	}
	cfg.setMapped(slinitKey, out)
	warns := []SystemdWarning{exNote(key, val, exLogfileOnly+"; slinit refills its bucket continuously, journald per interval")}
	if _, ok := exMapped(cfg, otherKey); !ok {
		cfg.setMapped(otherKey, otherDefault)
		warns = append(warns, exNote(key, val, fmt.Sprintf(
			"%s = %s added, journald's default, which systemd uses when the unit does not set it", otherKey, otherDefault)))
	}
	return warns
}

// mapLogFilterPatterns: LogFilterPatterns= → log-include, or with "~"
// log-exclude, one line each. systemd keeps a line only if it matches
// an allowed pattern (when there is one) and no denied one, which is how
// slinit combines the two. journald compiles a pattern with no capital
// letter in it caselessly (PATTERN_COMPILE_CASE_AUTO), so such a pattern
// gets (?i). A pattern Go's RE2 cannot compile would fail the start in
// slinit, so it is dropped here.
func mapLogFilterPatterns(cfg *SystemdConfig, key, val string) []SystemdWarning {
	v := strings.TrimSpace(val)
	if v == "" {
		cfg.dropMapped("log-include")
		cfg.dropMapped("log-exclude")
		return nil
	}
	slinitKey, pat := "log-include", v
	if rest, ok := strings.CutPrefix(v, "~"); ok {
		slinitKey, pat = "log-exclude", rest
		if pat == "" {
			return []SystemdWarning{exIgnored(key, val, errors.New("\"~\" with no pattern"))}
		}
	}
	if why := exLineSafe(pat); why != "" {
		return []SystemdWarning{exIgnored(key, val, errors.New(why))}
	}
	if !strings.ContainsFunc(pat, func(r rune) bool { return r >= 'A' && r <= 'Z' }) {
		pat = "(?i)" + pat
	}
	if _, err := regexp.Compile(pat); err != nil {
		return []SystemdWarning{exIgnored(key, val, fmt.Errorf("a PCRE2 pattern Go's regexp cannot compile (%v)", err))}
	}
	for _, kv := range cfg.mapped {
		if kv.key == slinitKey && kv.value == pat {
			return nil // systemd keeps a set
		}
	}
	cfg.mapped = append(cfg.mapped, kvPair{slinitKey, pat})
	return []SystemdWarning{exNote(key, val, exLogfileOnly)}
}

// --- stdio ----------------------------------------------------------

// The three streams are recorded as they are read and settled once the
// whole unit is (exFinishStdio): each one's default and an "inherit"
// depend on the others (stdout inherits a TTY on stdin, stderr follows
// stdout, StandardInputText= turns the default stdin into data), and
// the last assignment of each wins wherever it comes, drop-ins
// included.

// exStream is one StandardInput=/Output=/Error= value systemd accepts.
type exStream struct {
	set  bool
	kind string // the value, or "file" for file:/append:/truncate:
	path string // the file, for kind "file"
	diff string // how slinit opens the file differently
	raw  string // the value as written
}

func (s exStream) isTTY() bool { return strings.HasPrefix(s.kind, "tty") }

// mapStandardInput records StandardInput=. A value systemd refuses
// keeps the earlier one.
func mapStandardInput(cfg *SystemdConfig, key, val string) []SystemdWarning {
	v := strings.TrimSpace(val)
	s := exStream{set: true, kind: v, raw: v}
	var warns []SystemdWarning
	switch {
	case v == "null" || v == "data" || v == "tty" || v == "tty-force" || v == "tty-fail" || v == "tty-graceful":
	case strings.HasPrefix(v, "file:"):
		p, w, err := exPath(cfg, key, strings.TrimPrefix(v, "file:"))
		if err != nil {
			return append(w, exIgnored(key, val, err))
		}
		s.kind, s.path, warns = "file", p, w
	case v == "socket" || v == "fd" || strings.HasPrefix(v, "fd:") || v == "broker" || v == "broker-log":
	default:
		return []SystemdWarning{exIgnored(key, val, fmt.Errorf("%q is not a standard input type", v))}
	}
	exStateOf(cfg).in = s
	return warns
}

// exOutputFile is the path of a file:, append: or truncate: output,
// with how its open differs from slinit's logfile, which appends.
func exOutputFile(cfg *SystemdConfig, key, v string) (path, diff string, warns []SystemdWarning, ok bool, err error) {
	var rest string
	switch {
	case strings.HasPrefix(v, "append:"):
		rest = strings.TrimPrefix(v, "append:")
	case strings.HasPrefix(v, "file:"):
		rest, diff = strings.TrimPrefix(v, "file:"), "systemd writes from the start of the file without truncating it; slinit appends"
	case strings.HasPrefix(v, "truncate:"):
		rest, diff = strings.TrimPrefix(v, "truncate:"), "systemd truncates the file at each start; slinit appends"
	default:
		return "", "", nil, false, nil
	}
	path, warns, err = exPath(cfg, key, rest)
	return path, diff, warns, true, err
}

// exOutputGap answers the output types slinit has no destination for.
// journal, the default, and inherit say nothing: the service's output
// goes where any converted unit's goes.
func exOutputGap(key, val, v string) ([]SystemdWarning, bool) {
	switch {
	case v == "journal" || v == "syslog" || v == "inherit":
		return nil, true
	case v == "kmsg" || v == "journal+console" || v == "kmsg+console" || v == "syslog+console":
		return []SystemdWarning{exNote(key, val, "has no slinit equivalent: slinit sends a service's output to a logfile "+
			"(StandardOutput=append:) or discards it")}, true
	case v == "socket" || v == "fd" || strings.HasPrefix(v, "fd:") || v == "broker" || v == "broker-log":
		return []SystemdWarning{exNote(key, val, "has no slinit equivalent: the output is discarded")}, true
	}
	return nil, false
}

// exOutputStream reads a StandardOutput= / StandardError= value; ok is
// false for one systemd refuses, which keeps the earlier one.
func exOutputStream(cfg *SystemdConfig, key, val string) (exStream, []SystemdWarning, bool) {
	v := strings.TrimSpace(val)
	p, diff, warns, isFile, err := exOutputFile(cfg, key, v)
	if err != nil {
		return exStream{}, append(warns, exIgnored(key, val, err)), false
	}
	if isFile {
		return exStream{set: true, kind: "file", path: p, diff: diff, raw: v}, warns, true
	}
	if _, isGap := exOutputGap(key, val, v); !isGap && v != "null" && v != "tty" {
		return exStream{}, []SystemdWarning{exIgnored(key, val, fmt.Errorf("%q is not a standard output type", v))}, false
	}
	return exStream{set: true, kind: v, raw: v}, nil, true
}

// mapStandardOutput records StandardOutput=: append:/file:/truncate: →
// logfile, which takes stderr along unless StandardError= sends it
// elsewhere, as systemd's default StandardError=inherit does; null →
// close-stdout; tty → tty-path.
func mapStandardOutput(cfg *SystemdConfig, key, val string) []SystemdWarning {
	s, warns, ok := exOutputStream(cfg, key, val)
	if ok {
		exStateOf(cfg).out = s
	}
	return warns
}

// mapStandardError records StandardError=: a file other than stdout's →
// stderr-logfile (a type = process setting); null → close-stderr, which
// slinit cannot honour once stdout goes to a logfile; inherit and the
// rest leave stderr with stdout.
func mapStandardError(cfg *SystemdConfig, key, val string) []SystemdWarning {
	s, warns, ok := exOutputStream(cfg, key, val)
	if ok {
		exStateOf(cfg).err = s
	}
	return warns
}

// exInheritable is exec_input_is_inheritable: a stdin systemd's
// StandardOutput= default (and inherit) passes on to stdout.
func exInheritable(in exStream) bool {
	return in.isTTY() || in.kind == "socket" || strings.HasPrefix(in.kind, "fd")
}

// exFinishStdio settles stdin, stdout, stderr and the TTY from the last
// value of each directive, as systemd does once the unit is loaded
// (service_fix_stdio). What it leaves unset — a stdout to the journal,
// the default — config.defaultToJournal turns into log-type = journal
// afterwards for a process service.
func exFinishStdio(cfg *SystemdConfig, st *exState) []SystemdWarning {
	var warns []SystemdWarning
	in := st.in
	if !in.set {
		in = exStream{kind: "null", raw: "null"}
	}
	if in.kind == "null" && len(st.data) > 0 {
		// StandardInputText=/Data= make the default stdin data, and an
		// explicit null too (systemd does not tell them apart).
		in = exStream{set: true, kind: "data", raw: "data"}
	}
	out := st.out
	if !out.set || out.kind == "inherit" {
		switch {
		case in.isTTY():
			out = exStream{set: true, kind: "tty", raw: "inherit"}
		case exInheritable(in):
			out = exStream{set: true, kind: in.kind, raw: "inherit"}
		default:
			out = exStream{kind: "journal", raw: "journal"}
		}
	}
	errOwn := st.err.set && st.err.kind != "inherit"
	errS := out
	if errOwn {
		errS = st.err
	}

	// stdin.
	switch {
	case in.kind == "null":
		if in.set {
			cfg.closeStdin = true
		}
	case in.kind == "data":
		if len(st.data) > 0 {
			cfg.setMapped("standard-input-data", base64.StdEncoding.EncodeToString(st.data))
		}
	case in.isTTY():
		// systemd makes the tty the controlling terminal; slinit does
		// so with options = unmask-intr, which is therefore set.
		cfg.unmaskIntr = true
		warns = append(warns, exNote("StandardInput", in.raw, "→ tty-path with options = unmask-intr, so the tty is the "+
			"service's controlling terminal, as in systemd"))
		if in.kind == "tty-graceful" {
			warns = append(warns, exNote("StandardInput", in.raw, "slinit has no fallback to /dev/null when there is no console"))
		}
	case in.kind == "file":
		cfg.setMapped("standard-input-file", literalDollars(in.path))
	case in.kind == "socket" && cfg.acceptTmpl:
		// socket-accept puts the connection on stdin, stdout and stderr.
	default:
		warns = append(warns, exNote("StandardInput", in.raw, "has no slinit equivalent; the service's stdin is /dev/null"))
	}
	if len(st.data) > 0 && in.kind != "data" {
		// slinit refuses a service with both; systemd leaves the data
		// unused.
		warns = append(warns, exNote("StandardInput", in.raw,
			"StandardInputText=/StandardInputData= unused: this stdin replaces them, as in systemd"))
	}

	// stdout.
	switch out.kind {
	case "file":
		cfg.setMapped("logfile", literalDollars(out.path))
		if out.diff != "" {
			warns = append(warns, exNote("StandardOutput", out.raw, out.diff))
		}
	case "null":
		cfg.setMapped("close-stdout", "yes")
	case "tty", "journal", "syslog":
	default:
		if out.kind == "socket" && cfg.acceptTmpl {
			break // the connection, as socket-accept gives it
		}
		gap, _ := exOutputGap("StandardOutput", out.raw, out.kind)
		warns = append(warns, gap...)
	}

	// stderr, where StandardError= sends it apart from stdout.
	if errOwn && !(errS.kind == out.kind && errS.path == out.path && errS.kind != "null") {
		withLog := out.kind == "file"
		switch errS.kind {
		case "file":
			switch {
			case cfg.svcType != "process":
				warns = append(warns, exWarn("StandardError", errS.raw, fmt.Sprintf(
					"dropped: stderr-logfile works for type = process only; this %s service's stderr is not written to it", cfg.svcType)))
			default:
				cfg.setMapped("stderr-logfile", literalDollars(errS.path))
				if errS.diff != "" {
					warns = append(warns, exNote("StandardError", errS.raw, errS.diff))
				}
			}
		case "null":
			if withLog {
				warns = append(warns, exWarn("StandardError", errS.raw, "not carried: slinit writes stderr to the logfile with stdout"))
			} else {
				cfg.setMapped("close-stderr", "yes")
			}
		case "tty":
		default:
			gap, _ := exOutputGap("StandardError", errS.raw, errS.kind)
			warns = append(warns, gap...)
			if withLog {
				warns = append(warns, exNote("StandardError", errS.raw, "slinit writes stderr to the logfile with stdout"))
			}
		}
	}

	// The TTY. slinit connects all three streams to tty-path; systemd
	// only the ones set to tty, and uses TTYPath= for those.
	ttyUsed := in.isTTY() || out.kind == "tty" || errS.kind == "tty"
	tty := st.ttyPath
	if tty == "" && ttyUsed {
		tty = "/dev/console"
	}
	if tty != "" {
		cfg.setMapped("tty-path", tty)
		if !(in.isTTY() && out.kind == "tty" && errS.kind == "tty") {
			what := "tty-path = " + tty
			if st.ttyPath != "" {
				what = "TTYPath=" + st.ttyPath + " → tty-path"
			}
			warns = append(warns, SystemdWarning{"NOTE", what + ": slinit connects stdin, stdout and stderr to it, where " +
				"systemd connects only the streams StandardInput=/StandardOutput=/StandardError= set to tty " +
				"(here " + exStreamNames(in, out, errS) + ")"})
		}
	}
	if st.utmpID != "" {
		cfg.setMapped("inittab-id", st.utmpID)
		if tty != "" {
			cfg.setMapped("inittab-line", strings.TrimPrefix(tty, "/dev/"))
		}
	}
	return warns
}

// exStreamNames lists where each stream goes in systemd, for a note.
func exStreamNames(in, out, errS exStream) string {
	name := func(s exStream) string {
		if s.kind == "file" {
			return s.path
		}
		return s.kind
	}
	return fmt.Sprintf("stdin %s, stdout %s, stderr %s", name(in), name(out), name(errS))
}

// exStdinDataMax keeps the standard-input-data line under the 64 KiB a
// slinit service file line may have (bufio.Scanner's limit in Parse).
const exStdinDataMax = 60000

// mapStandardInputData: StandardInputText= and StandardInputData= add
// to one buffer in systemd, a text line with its C escapes decoded,
// its specifiers expanded and a newline after it. slinit's
// standard-input-text takes no escapes, adds no newline and is replaced
// by a second "=" line, so the buffer is written as standard-input-data,
// base64, which carries any bytes exactly (exFinishStdio).
func mapStandardInputData(cfg *SystemdConfig, key, val string) []SystemdWarning {
	st := exStateOf(cfg)
	v := strings.TrimSpace(val)
	if v == "" {
		st.data = nil
		return nil
	}
	var add []byte
	var warns []SystemdWarning
	if key == "StandardInputText" {
		text, err := exCUnescape(v)
		if err != nil {
			return []SystemdWarning{exIgnored(key, val, err)}
		}
		text, warns = expandSpecifiers(cfg, key, text)
		add = append([]byte(text), '\n')
	} else {
		raw := strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, v)
		b, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			if b, err = base64.RawStdEncoding.DecodeString(raw); err != nil {
				return []SystemdWarning{exIgnored(key, val, errors.New("not valid base64"))}
			}
		}
		add = b
	}
	buf := append(slices.Clone(st.data), add...)
	if n := base64.StdEncoding.EncodedLen(len(buf)); n > exStdinDataMax {
		return append(warns, exIgnored(key, val, fmt.Errorf(
			"the standard input data would come to %d bytes of base64, more than a slinit line holds", n)))
	}
	st.data = buf
	return warns
}

// mapOpenFile: OpenFile=PATH[:NAME[:OPTIONS]] → open-file, same grammar.
// systemd reads the three fields with C escapes and checks them here,
// where slinit would fail the start. slinit's "=" replaces the list,
// so of several OpenFile= lines only the last one is kept.
func mapOpenFile(cfg *SystemdConfig, key, val string) []SystemdWarning {
	v := strings.TrimSpace(val)
	if v == "" {
		cfg.dropMapped("open-file")
		return nil
	}
	fields, err := exColonWordsUnescaped(v)
	if len(fields) == 4 && fields[3] == "" {
		// extract_many_words takes the colon after the third field
		// with it: "PATH:NAME:OPTIONS:" holds three.
		fields = fields[:3]
	}
	if err == nil && len(fields) > 3 {
		err = errors.New("more than three colon-separated fields")
	}
	if err != nil {
		return []SystemdWarning{exIgnored(key, val, err)}
	}
	for len(fields) < 3 {
		fields = append(fields, "")
	}
	path, name, opts := fields[0], fields[1], fields[2]
	if !strings.HasPrefix(path, "/") {
		return []SystemdWarning{exIgnored(key, val, fmt.Errorf("%q is not an absolute path", path))}
	}
	if strings.Contains(path, ":") {
		// An escaped one (\x3a): slinit splits the value at every colon.
		return []SystemdWarning{exIgnored(key, val, fmt.Errorf("%q holds a colon, which slinit's open-file cannot", path))}
	}
	if name == "" {
		name = filepath.Base(path)
	}
	if !exFdNameValid(name) {
		return []SystemdWarning{exIgnored(key, val, fmt.Errorf("%q is not a valid file descriptor name", name))}
	}
	var flags []string
	modes := 0
	if opts != "" {
		for _, o := range strings.Split(opts, ",") {
			switch o {
			case "":
				continue // extract_first_word coalesces the commas
			case "read-only", "append", "truncate":
				modes++
			case "graceful":
			default:
				return []SystemdWarning{exIgnored(key, val, fmt.Errorf("unknown option %q", o))}
			}
			if slices.Contains(flags, o) {
				return []SystemdWarning{exIgnored(key, val, fmt.Errorf("option %q given twice", o))}
			}
			flags = append(flags, o)
		}
	}
	if modes > 1 {
		return []SystemdWarning{exIgnored(key, val, errors.New("read-only, append and truncate exclude each other"))}
	}
	out := path + ":" + name
	if len(flags) > 0 {
		out += ":" + strings.Join(flags, ",")
	}
	if why := exLineSafe(out); why != "" {
		return []SystemdWarning{exIgnored(key, val, errors.New(why))}
	}
	out = literalDollars(out)
	if err := validateSlinitSetting("open-file", out); err != nil {
		return []SystemdWarning{exIgnored(key, val, err)}
	}
	var warns []SystemdWarning
	if prev, ok := exMapped(cfg, "open-file"); ok && prev != out {
		warns = append(warns, exWarn(key, val, fmt.Sprintf(
			"slinit's open-file takes one file in a converted unit, keeping the last (dropped: %q)", prev)))
	}
	cfg.setMapped("open-file", out)
	diff := "slinit cannot connect to an AF_UNIX socket at the path"
	if !slices.Contains(flags, "read-only") {
		diff = "slinit creates the file (0644) when it is missing, where systemd fails the start; " + diff
	}
	if slices.Contains(flags, "graceful") {
		diff += "; on a failed open slinit passes /dev/null under the name, where systemd passes no descriptor"
	}
	return append(warns, exNote(key, val, diff))
}

// exColonWordsUnescaped is extract_many_words with ":" and
// EXTRACT_DONT_COALESCE_SEPARATORS|EXTRACT_CUNESCAPE, as open_file_parse
// reads OpenFile=: a backslash takes a C escape, or a colon or
// backslash as it is.
func exColonWordsUnescaped(s string) ([]string, error) {
	var words []string
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
			if i == len(s) {
				return nil, errors.New("trailing backslash")
			}
			if out, n, ok := cunescapeOne(s[i:]); ok {
				b.Write(out)
				i += n - 1
			} else {
				return nil, fmt.Errorf("unknown escape sequence \\%c", s[i])
			}
		case ':':
			words = append(words, b.String())
			b.Reset()
		default:
			b.WriteByte(s[i])
		}
	}
	return append(words, b.String()), nil
}

// exFdNameValid is fdname_is_valid: printable ASCII but ':', up to 255.
func exFdNameValid(s string) bool {
	if len(s) > 255 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < ' ' || s[i] >= 127 || s[i] == ':' {
			return false
		}
	}
	return true
}

// --- credentials ----------------------------------------------------

// exCredNameValid is credential_name_valid: a valid file name and a
// valid fd name.
func exCredNameValid(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.Contains(s, "/") && exFdNameValid(s)
}

// exCredName reads the credential name of a LoadCredential= /
// SetCredential= / ImportCredential= value, with its specifiers
// expanded: the text up to the first unescaped colon. rest is what
// follows the colon, empty if there is none.
func exCredName(cfg *SystemdConfig, key, v string) (name, rest string, warns []SystemdWarning, err error) {
	w, rest, err := exColonWord(v)
	if err != nil {
		return "", "", nil, err
	}
	name, warns = expandSpecifiers(cfg, key, w)
	if !exCredNameValid(name) || strings.TrimSpace(name) != name {
		return "", "", warns, fmt.Errorf("%q is not a valid credential name", name)
	}
	return name, rest, warns, nil
}

// exCred is one LoadCredential= or SetCredential=, by name.
type exCred struct {
	name  string
	value string // the file (load) or the decoded value (set)
	raw   string // the assignment as written
}

// exPutCred records a credential as systemd's exec_context_put_*_credential
// does, replacing one of the same kind and name.
func exPutCred(list []exCred, c exCred) []exCred {
	for i := range list {
		if list[i].name == c.name {
			list[i] = c
			return list
		}
	}
	return append(list, c)
}

// mapLoadCredential: LoadCredential=ID:PATH → load-credential, for an
// absolute PATH to a file. systemd also reads every file under a
// directory, connects to an AF_UNIX socket, and takes a relative name
// (or ID alone) as a credential slinit itself received or one in a
// credential store; slinit's load-credential reads one regular file.
func mapLoadCredential(cfg *SystemdConfig, key, val string) []SystemdWarning {
	st := exStateOf(cfg)
	v := strings.TrimSpace(val)
	if v == "" {
		// Resets the LoadCredential= list only; SetCredential= keeps its own.
		st.load = nil
		return nil
	}
	name, rest, warns, err := exCredName(cfg, key, v)
	if err != nil {
		return append(warns, exIgnored(key, val, err))
	}
	src := name
	if rest != "" {
		var w []SystemdWarning
		src, w = expandSpecifiers(cfg, key, rest)
		warns = append(warns, w...)
	}
	if !strings.HasPrefix(src, "/") {
		if !exCredNameValid(src) {
			return append(warns, exIgnored(key, val, fmt.Errorf("%q is neither an absolute path nor a credential name", src)))
		}
		hint := ""
		if src == name {
			hint = fmt.Sprintf("; use import-credential = %s", name)
		}
		return append(warns, exWarn(key, val, fmt.Sprintf(
			"dropped: it takes credential %q from the credentials slinit itself received or a credential store, "+
				"and slinit's load-credential reads only a file%s", src, hint)))
	}
	if filepath.Clean(src) != src || strings.Contains(src, "/./") || strings.HasSuffix(src, "/.") {
		return append(warns, exIgnored(key, val, fmt.Errorf("%q is not a normalized path", src)))
	}
	if why := exLineSafe(src); why != "" {
		return append(warns, exIgnored(key, val, errors.New(why)))
	}
	if fi, err := os.Stat(src); err == nil && !fi.Mode().IsRegular() {
		return append(warns, exWarn(key, val, fmt.Sprintf(
			"dropped: %s is no regular file; systemd reads a directory's files or connects to a socket, "+
				"slinit's load-credential reads one regular file", src)))
	}
	st.load = exPutCred(st.load, exCred{name, src, v})
	return warns
}

// mapSetCredential: SetCredential=ID:VALUE → set-credential. systemd
// decodes C escapes in VALUE; slinit takes it as written, with its $
// expanded, so the decoded value is written back with each $ doubled.
// What a line of a slinit file cannot hold (a newline, which systemd's
// own documentation suggests for a trailing one, a NUL, blanks at the
// ends) drops the credential.
func mapSetCredential(cfg *SystemdConfig, key, val string) []SystemdWarning {
	st := exStateOf(cfg)
	v := strings.TrimSpace(val)
	if v == "" {
		st.set = nil
		return nil
	}
	name, rest, warns, err := exCredName(cfg, key, v)
	if err == nil && rest == "" {
		err = errors.New("no value after the credential name")
	}
	if err != nil {
		return append(warns, exIgnored(key, val, err))
	}
	data, err := exCUnescape(rest)
	if err == nil {
		if why := exLineSafe(data); why != "" {
			err = errors.New(why)
		}
	}
	if err != nil {
		return append(warns, exIgnored(key, val, fmt.Errorf("%v — write the value to a file and use LoadCredential=", err)))
	}
	st.set = exPutCred(st.set, exCred{name, data, v})
	return warns
}

// exFinishCreds writes the credentials. systemd uses a SetCredential=
// only when the LoadCredential= of its name finds no file; slinit loads
// both, in file order, a later one over an earlier one, and fails the
// start on a missing file. So every set-credential goes before the
// load-credentials, and a load-credential whose file is missing where
// the unit is converted is left out when a SetCredential= backs it: the
// direct loader converts on the host that runs the service, where
// systemd would take the value.
func exFinishCreds(cfg *SystemdConfig, st *exState) []SystemdWarning {
	var warns []SystemdWarning
	drop := map[string]bool{}
	for _, s := range st.set {
		for _, l := range st.load {
			if l.name != s.name {
				continue
			}
			if fi, err := os.Stat(l.value); err == nil && fi.Mode().IsRegular() {
				warns = append(warns, exNote("LoadCredential", l.raw, "with SetCredential="+s.name+":… as its fallback: "+
					"the file is there, so slinit loads it over the value, but should it go missing slinit fails the start, "+
					"where systemd falls back to the value"))
			} else {
				drop[l.name] = true
				warns = append(warns, exNote("LoadCredential", l.raw, "dropped: "+l.value+" is missing here, so systemd "+
					"uses SetCredential="+s.name+":… instead, and slinit would fail the start; set-credential kept"))
			}
		}
		cfg.mapped = append(cfg.mapped, kvPair{"set-credential", s.name + ":" + literalDollars(s.value)})
	}
	for _, l := range st.load {
		if !drop[l.name] {
			cfg.mapped = append(cfg.mapped, kvPair{"load-credential", l.name + ":" + literalDollars(l.value)})
		}
	}
	// import-credential: "=" replaces slinit's list, "+=" adds to it.
	for i, g := range st.imports {
		key := "import-credential"
		if i > 0 {
			key += "+"
		}
		cfg.mapped = append(cfg.mapped, kvPair{key, g})
	}
	return warns
}

// mapImportCredential: ImportCredential=GLOB → import-credential. Both
// search the credentials the manager received and the plain credential
// stores; systemd also the .encrypted ones. A ":NEWNAME" rename has no
// slinit form. Every pattern is kept (exFinishCreds writes them).
func mapImportCredential(cfg *SystemdConfig, key, val string) []SystemdWarning {
	st := exStateOf(cfg)
	v := strings.TrimSpace(val)
	if v == "" {
		st.imports = nil
		return nil
	}
	w, rest, err := exColonWord(v)
	if err != nil {
		return []SystemdWarning{exIgnored(key, val, err)}
	}
	glob, warns := expandSpecifiers(cfg, key, w)
	if !exCredGlobValid(glob) {
		return append(warns, exIgnored(key, val, fmt.Errorf("%q is not a valid credential name or glob", glob)))
	}
	if strings.Contains(glob, `\`) {
		return append(warns, exIgnored(key, val, fmt.Errorf("%q holds a backslash, which slinit's glob reads as an escape", glob)))
	}
	if rest != "" {
		return append(warns, exWarn(key, val, "dropped: slinit's import-credential cannot rename what it imports"))
	}
	glob = literalDollars(glob)
	if err := validateSlinitSetting("import-credential", glob); err != nil {
		return append(warns, exIgnored(key, val, err))
	}
	if !slices.Contains(st.imports, glob) {
		st.imports = append(st.imports, glob) // systemd keeps a set
	}
	return warns
}

// exCredGlobValid is credential_glob_valid: a credential name, or one
// with a single "*" at its end, or "*" alone.
func exCredGlobValid(s string) bool {
	n := strings.IndexAny(s, "*?[]")
	if n < 0 {
		return exCredNameValid(s)
	}
	if s[n:] != "*" {
		return false
	}
	return n == 0 || exCredNameValid(s[:n])
}

// --- environment ----------------------------------------------------

// mapUnsetEnvironment: UnsetEnvironment= → unset-environment, both
// applied last, to the whole environment. systemd reads the words with
// quotes and C escapes and also takes NAME=VALUE, which removes the
// variable only while it has that value; slinit removes names only, so
// such an entry is left out rather than widened to the whole name.
func mapUnsetEnvironment(cfg *SystemdConfig, key, val string) []SystemdWarning {
	v := strings.TrimSpace(val)
	if v == "" {
		cfg.dropMapped("unset-environment")
		return nil
	}
	var names []string
	var warns []SystemdWarning
	for p := v; ; {
		word, rest, found, err := extractSystemdWord(p, false)
		if err != nil {
			warns = append(warns, exWarn(key, val, fmt.Sprintf("%v — the rest of the line ignored, as systemd does", err)))
			break
		}
		if !found {
			break
		}
		p = rest
		word, w := expandSpecifiers(cfg, key, word)
		warns = append(warns, w...)
		name, _, isAssign := strings.Cut(word, "=")
		switch {
		case !isAssign && isEnvName(word):
			names = append(names, word)
		case isAssign && isEnvName(name):
			warns = append(warns, exWarn(key, val, fmt.Sprintf(
				"%q left out: it unsets %s only while it has that value, and slinit's unset-environment takes names only", word, name)))
		default:
			warns = append(warns, exIgnored(key, val, fmt.Errorf("%q is no variable name", word)))
		}
	}
	if len(names) > 0 {
		cfg.appendMapped("unset-environment", strings.Join(names, " "))
	}
	return warns
}

// --- ExecStopPost=, ExecReload= ---------------------------------------

// exFinishScript drops the two arguments slinit appends to a
// finish-command (the exit code and the signal number) and runs the
// rest. systemd passes ExecStopPost= no such arguments: written as it
// is, "rm -f /run/foo.pid" would also remove "0" and "15" in the
// working directory. In slinit's $ syntax, every $ doubled.
var exFinishScript = literalDollars(`n=$#; while [ "$n" -gt 2 ]; do set -- "$@" "$1"; shift; n=$((n-1)); done; shift 2; exec "$@"`)

func exFinishArgv(argv []string) []string {
	return append([]string{"/bin/sh", "-c", exFinishScript, "sh"}, argv...)
}

// mapExecStopPost: ExecStopPost= → finish-command, which slinit runs
// after each exit of the main process, before a restart, as systemd runs
// ExecStopPost=. slinit takes one finish-command; a later line is
// dropped, as for ExecStartPre=.
func mapExecStopPost(cfg *SystemdConfig, key, val string) []SystemdWarning {
	if strings.TrimSpace(val) == "" {
		cfg.dropMapped("finish-command")
		return nil
	}
	cmds, warns, err := parseExecLine(cfg, key, val)
	if err != nil {
		cfg.errs = append(cfg.errs, err.Error())
		return warns
	}
	for _, c := range cmds {
		fargv := c.cmd.argv
		rt := runtimeVars(c.refs)
		if rt {
			fargv = runtimeShellArgv(fargv)
		}
		value, ok := slinitCommandValue(exFinishArgv(fargv))
		if !ok {
			warns = append(warns, exWarn(key, val, "an argument holds a newline, which a slinit command setting cannot — command dropped"))
			continue
		}
		if prev, ok := exMapped(cfg, "finish-command"); ok {
			warns = append(warns, exWarn(key, val, fmt.Sprintf(
				"slinit takes one finish-command, keeping the first, %q (dropped: %q)", prev, value)))
			continue
		}
		priv := c.fullPriv || c.noSetuid
		value = privPrefix(priv) + value
		if err := validateSlinitSetting("finish-command", value); err != nil {
			warns = append(warns, exIgnored(key, val, err))
			continue
		}
		warns = append(warns, warnExecVarsRT(key, c.refs, c.bad, rt)...)
		warns = append(warns, warnArgv0(key, c.cmd.argv0)...)
		switch {
		case c.fullPriv:
			warns = append(warns, exNote(key, val, `"+" — carried as slinit's "+": run as root, without the service's run-as`))
		case c.noSetuid:
			warns = append(warns, exNote(key, val, `"!" (sandbox, but no User=/Group=) — written with slinit's "+": it runs as root, as in systemd; `+
				`slinit gives a finish-command no sandbox either way`))
		}
		if c.viaShell {
			warns = append(warns, exNote(key, val, `"|" — run with /bin/sh -c; systemd uses the login shell of User=, /bin/sh when it has none`))
		}
		warns = append(warns, exNote(key, val, "→ finish-command, run after each exit of the main process, but not when it could not be "+
			"started at all; bounded by hook-timeout (default 5 s), not TimeoutStopSec=; slinit only logs its failure, "+
			"and sets no $SERVICE_RESULT/$EXIT_CODE/$EXIT_STATUS"))
		cfg.setMapped("finish-command", value)
	}
	return warns
}

// exFinishStopPost: slinit runs a finish-command for type = process
// only. Type= may come after ExecStopPost=, so this is decided here.
func exFinishStopPost(cfg *SystemdConfig) []SystemdWarning {
	if _, ok := cfg.hasMapped("finish-command"); !ok || cfg.svcType == "process" {
		return nil
	}
	cfg.dropMapped("finish-command")
	return []SystemdWarning{{"WARN", fmt.Sprintf(
		"ExecStopPost= dropped: slinit runs a finish-command for type = process only; this %s service would not run it", cfg.svcType)}}
}

// mapExecReload: the ExecReload= that only signals the main process,
// "kill -HUP $MAINPID", is slinit's reload-signal; any other command is
// its reload-command (since 4.1), which runs in the service's context
// as systemd runs ExecReload=. slinit runs one: a second command is
// dropped with a warning. Its variables are expanded when it runs, from
// the service's environment, where slinit puts $MAINPID too. The signal is settled with
// ReloadSignal= in exFinishReload; an empty assignment drops what
// ExecReload= said, and nothing else.
func mapExecReload(cfg *SystemdConfig, key, val string) []SystemdWarning {
	st := exStateOf(cfg)
	if strings.TrimSpace(val) == "" {
		st.execReloadSignal, st.execReloadLine, st.execReloadWarns = "", "", nil
		cfg.dropMapped("reload-command")
		return nil
	}
	cmds, warns, err := parseExecLine(cfg, key, val)
	if err != nil {
		cfg.errs = append(cfg.errs, err.Error())
		return warns
	}
	for _, c := range cmds {
		mainpid := slices.Contains(c.refs, "$MAINPID") || slices.Contains(c.refs, "${MAINPID}")
		if mainpid {
			if sig, ok := killMainPID(c.cmd.argv); ok {
				st.execReloadSignal, st.execReloadLine = sig, val
				continue
			}
		}
		argv := c.cmd.argv
		// A reload-command has $MAINPID in its environment, so its
		// variables, that one included, are expanded when it runs.
		rt := runtimeVars(c.refs) || mainpid
		if rt {
			argv = runtimeShellArgv(argv)
		}
		if c.ignoreFailure {
			argv = ignoreFailureArgv(argv)
		}
		value, ok := slinitCommandValue(argv)
		if !ok {
			warns = append(warns, exWarn(key, val, "an argument holds a newline, which a slinit command setting cannot — command dropped"))
			continue
		}
		if prev, ok := exMapped(cfg, "reload-command"); ok {
			warns = append(warns, exWarn(key, val, fmt.Sprintf(
				"slinit runs one reload-command, keeping the first, %q (dropped: %q)", prev, value)))
			continue
		}
		value = privPrefix(c.fullPriv) + value
		if err := validateSlinitSetting("reload-command", value); err != nil {
			warns = append(warns, exIgnored(key, val, err))
			continue
		}
		warns = append(warns, warnExecVarsRT(key, c.refs, c.bad, rt)...)
		cfg.setMapped("reload-command", value)
	}
	st.execReloadWarns = append(st.execReloadWarns, warns...)
	return nil
}

// mapReloadSignal records ReloadSignal=; an empty assignment resets it
// to the default, as config_parse_signal does.
func mapReloadSignal(cfg *SystemdConfig, key, val string) []SystemdWarning {
	st := exStateOf(cfg)
	v := strings.TrimSpace(val)
	if v == "" {
		st.reloadSignal, st.reloadSignalRaw = "", ""
		return nil
	}
	sig, err := lcSignalFrom(v)
	if err != nil {
		return []SystemdWarning{exIgnored(key, val, err)}
	}
	st.reloadSignal, st.reloadSignalRaw = sig, v
	return nil
}

// exSameSignal reports whether two signal spellings name one signal.
func exSameSignal(a, b string) bool {
	x, err1 := parseSignal(a)
	y, err2 := parseSignal(b)
	return err1 == nil && err2 == nil && x == y
}

// exFinishReload picks the one signal slinit's reload-signal sends. In
// systemd a reload runs ExecReload= when there is one; Type=notify-reload
// then sends ReloadSignal= (SIGHUP by default) as well, and any other
// type ignores ReloadSignal=. So with notify-reload the signal is
// ReloadSignal='s, otherwise the one ExecReload= sends.
func exFinishReload(cfg *SystemdConfig, st *exState) []SystemdWarning {
	warns := slices.Clone(st.execReloadWarns)
	if v, ok := exMapped(cfg, "reload-command"); ok && cfg.svcType != "process" && cfg.svcType != "bgprocess" {
		cfg.dropMapped("reload-command")
		warns = append(warns, exWarn("ExecReload", v, fmt.Sprintf(
			"dropped: slinit runs a reload-command for a process or bgprocess service, and this one is %s", cfg.svcType)))
	}
	es, rs := st.execReloadSignal, st.reloadSignal
	switch {
	case cfg.unitType == "notify-reload":
		sig := rs
		if sig == "" {
			sig = "SIGHUP"
		}
		cfg.setMapped("reload-signal", sig)
		if es != "" && !exSameSignal(es, sig) {
			warns = append(warns, exNote("ExecReload", st.execReloadLine, fmt.Sprintf(
				"systemd sends SIG%s with it and then %s, as Type=notify-reload does; slinit's reload-signal = %s sends %s only",
				strings.TrimPrefix(es, "SIG"), sig, sig, sig)))
		}
	case es != "":
		cfg.setMapped("reload-signal", es)
		warns = append(warns, exNote("ExecReload", st.execReloadLine, fmt.Sprintf(
			"→ reload-signal = %s; send it with slinitctl reload-signal (slinitctl reload re-reads the service file)", es)))
		if rs != "" && !exSameSignal(rs, es) {
			warns = append(warns, exNote("ReloadSignal", st.reloadSignalRaw,
				"ignored: systemd uses it with Type=notify-reload only, and the reload here is ExecReload='s "+es))
		}
	case rs != "":
		cfg.setMapped("reload-signal", rs)
	}
	return warns
}
