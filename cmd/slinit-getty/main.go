// slinit-getty — minimal login-prompt binary. finit-parity for a
// built-in getty: reduce the util-linux / agetty dependency for
// embedded slinit installs where BusyBox is present but agetty is
// not, or where operators want a single-vendor init+getty stack.
//
// Args (match finit's getty tool byte-for-byte so existing
// tty entries transfer cleanly):
//
//	slinit-getty [-p] TTY [BAUD [TERM]]
//
//	-p       pass "-p" to /bin/login (preserve caller env)
//	TTY      device path — /dev/ttyS0, /dev/tty1, or a bare
//	         "ttyS0" (the /dev/ prefix is auto-prepended).
//	BAUD     serial baud rate as a decimal integer. Ignored
//	         for VTs (kernel manages VT termios). Optional.
//	TERM     value to export as $TERM before exec of login.
//	         Optional; defaults to whatever the caller set.
//
// Flow:
//  1. Open TTY as new controlling terminal (setsid + TIOCSCTTY),
//     reclaimed as agetty does: root:tty 0620 (0600 with no tty
//     group), then vhangup(2) and a fresh open.
//  2. dup2 into fd 0/1/2.
//  3. Configure termios for canonical line input with echo.
//  4. Render /etc/issue with the classic backslash escapes.
//  5. Print "<host> login: " prompt, read username line.
//  6. execve /bin/login [-p] -- USERNAME.
//
// Fallback chain if /bin/login is missing:
//
//	/sbin/login → /usr/bin/login → sulogin → exit 1.
//
// finit's getty.c ends the chain with /bin/sh; slinit-getty stops at
// sulogin, which asks for the root password, and otherwise exits so
// the supervisor respawns it.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// loginNameMax bounds the username field length. LOGIN_NAME_MAX
	// on Linux is 256; the finit code uses 64 as the minimum via
	// LOGIN_NAME_MIN. Match Linux's runtime value where possible.
	loginNameMax = 256

	issuePath = "/etc/issue"
)

// loginPathCandidates is the search order for the login binary.
// finit uses _PATH_LOGIN = /bin/login with a hard fallback to
// _PATH_SULOGIN then _PATH_BSHELL; slinit walks the same shape
// but with the modern Alpine/Debian install locations checked.
var loginPathCandidates = []string{
	"/bin/login",
	"/sbin/login",
	"/usr/bin/login",
}

// rescueShellCandidates run when login cannot. Only sulogin, which
// authenticates: finit's last resort, a plain /bin/sh, gave anyone at
// the terminal a root shell whenever login failed to exec.
var rescueShellCandidates = []string{
	"/sbin/sulogin",
	"/bin/sulogin",
	"/usr/sbin/sulogin",
}

// version is set at build time with -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	// --version, alone: checked before anything else runs, so no other
	// argument changes meaning. The value is stamped by the package
	// build (-X main.version=...).
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Printf("slinit-getty version %s\n", version)
		return
	}
	var passEnv bool
	flag.BoolVar(&passEnv, "p", false, "pass -p to /bin/login (preserve caller environment)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: slinit-getty [-p] TTY [BAUD [TERM]]")
	}
	flag.Parse()
	args := flag.Args()
	if len(args) < 1 {
		flag.Usage()
		os.Exit(1)
	}
	ttyArg := args[0]
	var baud, term string
	if len(args) > 1 {
		baud = args[1]
	}
	if len(args) > 2 {
		term = args[2]
	}

	if err := run(ttyArg, baud, term, passEnv); err != nil {
		fmt.Fprintf(os.Stderr, "slinit-getty: %v\n", err)
		os.Exit(1)
	}
}

func run(ttyArg, baud, term string, passEnv bool) error {
	ttyPath := ttyArg
	if !strings.HasPrefix(ttyPath, "/") {
		ttyPath = "/dev/" + ttyPath
	}

	// Take a new session so the tty we open becomes our controlling
	// terminal without racing whatever session the parent (slinit)
	// was in. Best-effort — if we already own a fresh session
	// (spawned via Setsid at fork), setsid returns EPERM which is
	// fine.
	if _, err := syscall.Setsid(); err != nil {
		// EPERM when already a session leader is fine. A process-group
		// leader that is not one (a service without new-session = yes)
		// cannot take a session, so the tty will not become the
		// controlling terminal of getty or of the login it runs.
		if sid, _ := unix.Getsid(0); sid != os.Getpid() {
			fmt.Fprintf(os.Stderr, "slinit-getty: cannot start a session (%v): %s will not "+
				"be the controlling terminal; run this service with new-session = yes\n", err, ttyPath)
		}
	}

	// Open the tty for reading + writing. O_NOCTTY delays the
	// controlling-terminal assignment so we can decide when to
	// TIOCSCTTY explicitly below (some kernels reject TIOCSCTTY on
	// an already-controlled tty).
	fd, err := syscall.Open(ttyPath, syscall.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", ttyPath, err)
	}

	// Reclaim the tty from its previous user, as agetty does: root
	// owns it again, and a vhangup revokes every descriptor a process
	// left from the last session still holds, so nothing of theirs can
	// read the next login.
	if err := reclaimTTY(fd, ttyGroup()); err != nil {
		fmt.Fprintf(os.Stderr, "slinit-getty: reclaim %s: %v\n", ttyPath, err)
	}
	if fd, err = hangupAndReopen(fd, ttyPath); err != nil {
		return fmt.Errorf("reopen %s after vhangup: %w", ttyPath, err)
	}

	// Wire the tty as fd 0/1/2 for both this process AND the
	// eventual login exec. dup2 leaves the source fd open; close it
	// only when it's not one of 0/1/2 already.
	for _, target := range []int{0, 1, 2} {
		if err := syscall.Dup2(fd, target); err != nil {
			return fmt.Errorf("dup2 tty to fd %d: %w", target, err)
		}
	}
	if fd > 2 {
		_ = syscall.Close(fd)
	}

	// Now claim the tty as our controlling terminal. `1` = force
	// steal from prior controlling process; needed when the parent
	// (slinit) is holding it via setctty on its own session.
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, 0, uintptr(unix.TIOCSCTTY), 1); errno != 0 {
		// Non-fatal — kernel may reject the steal on a fresh tty
		// that hasn't had a session yet. Login binary re-tries.
	}

	// Canonical line mode with echo — standard getty setup. Skip
	// the raw+baud dance for VTs (VT drivers ignore baud); apply
	// baud if the caller asked for it explicitly and the fd is a
	// serial line.
	if err := configureTermios(0, baud); err != nil {
		// Log and continue — kernel default termios is usually
		// close enough for a login prompt to work.
		fmt.Fprintf(os.Stderr, "slinit-getty: termios setup: %v\n", err)
	}

	// Render /etc/issue if present. Missing file is a silent
	// no-op — some minimal images ship without one.
	if data, err := os.ReadFile(issuePath); err == nil {
		expanded := expandIssue(string(data), ttyPath)
		fmt.Print(expanded)
	}

	// Read the username with a bounded scanner. If the user hits
	// Ctrl-D or EOF we loop back to the issue banner rather than
	// crashing — matches finit's `goto restart`.
	name, err := readLoginName()
	if err != nil {
		// EOF or unrecoverable read — bail with non-zero so the
		// respawn supervisor cycles us fresh.
		return fmt.Errorf("read login name: %w", err)
	}

	if term != "" {
		os.Setenv("TERM", term)
	}

	return execLogin(name, passEnv, syscall.Exec)
}

// execLogin replaces getty with login(1) for name. When no login binary
// exists or its exec fails (ETXTBSY mid-upgrade, ENOMEM), it runs
// sulogin, which asks for the root password, and otherwise returns an
// error so getty exits and its supervisor respawns it. It never starts
// a shell without authentication: that handed whoever sat at the
// terminal a root shell.
func execLogin(name string, passEnv bool, execve func(string, []string, []string) error) error {
	loginErr := fmt.Errorf("none of %v found", loginPathCandidates)
	if loginBin := firstExisting(loginPathCandidates); loginBin != "" {
		// argv[0] = login (POSIX-standard invocation name).
		argv := []string{"login"}
		if passEnv {
			argv = append(argv, "-p")
		}
		argv = append(argv, "--", name)
		err := execve(loginBin, argv, os.Environ())
		if err == nil {
			return nil // only a stand-in execve returns on success
		}
		loginErr = fmt.Errorf("exec %s: %w", loginBin, err)
	}
	fmt.Fprintf(os.Stderr, "slinit-getty: cannot run login: %v\n", loginErr)

	sulogin := firstExisting(rescueShellCandidates)
	if sulogin == "" {
		return fmt.Errorf("cannot run login (%v) and no sulogin found (tried %v)", loginErr, rescueShellCandidates)
	}
	fmt.Fprintf(os.Stderr, "slinit-getty: running %s instead\n", sulogin)
	if err := execve(sulogin, []string{sulogin}, os.Environ()); err != nil {
		return fmt.Errorf("cannot run login (%v) nor %s: %w", loginErr, sulogin, err)
	}
	return nil
}

// ttyGroup is the gid of the tty group, or 0 when there is none.
func ttyGroup() int {
	g, err := user.LookupGroup("tty")
	if err != nil {
		return 0
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0
	}
	return gid
}

// ttyMode is agetty's: 0620 when a tty group can write(1) to the user,
// 0600 when there is none.
func ttyMode(gid int) uint32 {
	if gid != 0 {
		return 0o620
	}
	return 0o600
}

// reclaimTTY gives the tty back to root and the tty group before the
// prompt, as agetty does: the last session's login made it the user's.
func reclaimTTY(fd, gid int) error {
	if err := unix.Fchown(fd, 0, gid); err != nil {
		return fmt.Errorf("fchown: %w", err)
	}
	if err := unix.Fchmod(fd, ttyMode(gid)); err != nil {
		return fmt.Errorf("fchmod: %w", err)
	}
	return nil
}

// hangupAndReopen makes fd's tty our controlling terminal, hangs it up
// with vhangup(2) so every other descriptor on it goes dead, and opens
// it afresh. vhangup sends SIGHUP to our own session too, so SIGHUP is
// ignored around it and restored before login inherits it. Without
// the privilege for vhangup the tty is kept as it is.
func hangupAndReopen(fd int, ttyPath string) (int, error) {
	_ = unix.IoctlSetInt(fd, unix.TIOCSCTTY, 1)
	signal.Ignore(syscall.SIGHUP)
	defer signal.Reset(syscall.SIGHUP)
	if _, _, errno := unix.Syscall(unix.SYS_VHANGUP, 0, 0, 0); errno != 0 {
		fmt.Fprintf(os.Stderr, "slinit-getty: vhangup %s: %v\n", ttyPath, errno)
		return fd, nil
	}
	_ = syscall.Close(fd)
	return syscall.Open(ttyPath, syscall.O_RDWR|syscall.O_NOCTTY, 0)
}

// configureTermios sets canonical + echo mode on fd, and if `baud`
// is a recognised serial rate applies it. Standard getty defaults.
func configureTermios(fd int, baud string) error {
	var t unix.Termios
	if err := ioctlTermios(fd, unix.TCGETS, &t); err != nil {
		return err
	}
	// Input: keep CR-to-NL translation + ignore break. Reject
	// parity errors silently rather than injecting substitute char.
	t.Iflag = t.Iflag&^unix.IGNBRK&^unix.INLCR&^unix.PARMRK&^unix.ISTRIP |
		unix.ICRNL | unix.IXON
	// Output: post-process newlines to CR-LF so the terminal renders
	// slinit's writes correctly on serial.
	t.Oflag = t.Oflag&^unix.OLCUC | unix.OPOST | unix.ONLCR
	// Line discipline: canonical (line-buffered) input, echo user's
	// keystrokes, echo NL as NL, allow signals (^C / ^\ / ^Z), let
	// the kernel handle backspace erasure.
	t.Lflag = t.Lflag&^unix.NOFLSH&^unix.TOSTOP&^unix.ECHOPRT |
		unix.ICANON | unix.ISIG | unix.ECHO | unix.ECHOE | unix.ECHOK | unix.ECHOKE | unix.ECHONL | unix.IEXTEN
	// Character format: 8-bit chars, one stop bit, enable receiver.
	t.Cflag = t.Cflag&^unix.PARENB&^unix.CSIZE | unix.CREAD | unix.CS8 | unix.HUPCL

	if baud != "" {
		if speed, ok := baudLookup[baud]; ok {
			// Set both input and output speeds — for serial ports
			// they must match; for VTs the kernel ignores.
			t.Ispeed = speed
			t.Ospeed = speed
			// Older kernels also read baud from Cflag CBAUD bits;
			// setting Ispeed/Ospeed via TCSETS covers modern paths.
		}
	}

	return ioctlTermios(fd, unix.TCSETS, &t)
}

// ioctlTermios wraps the TCGETS/TCSETS ioctl calls used above.
// x/sys/unix has IoctlSetTermios / IoctlGetTermios helpers but they
// wrap the same syscall — keep the call site inline so a reader
// sees the full termios flow without hunting through the package.
func ioctlTermios(fd int, req uint, t *unix.Termios) error {
	if req == unix.TCGETS {
		got, err := unix.IoctlGetTermios(fd, req)
		if err != nil {
			return err
		}
		*t = *got
		return nil
	}
	return unix.IoctlSetTermios(fd, req, t)
}

// baudLookup maps the decimal baud string finit accepts to the
// termios speed_t constant. Anything outside this set is silently
// ignored — VTs don't need a baud rate and asking for a bogus one
// on serial produces gibberish rather than an error.
var baudLookup = map[string]uint32{
	"0":       unix.B0,
	"50":      unix.B50,
	"75":      unix.B75,
	"110":     unix.B110,
	"134":     unix.B134,
	"150":     unix.B150,
	"200":     unix.B200,
	"300":     unix.B300,
	"600":     unix.B600,
	"1200":    unix.B1200,
	"1800":    unix.B1800,
	"2400":    unix.B2400,
	"4800":    unix.B4800,
	"9600":    unix.B9600,
	"19200":   unix.B19200,
	"38400":   unix.B38400,
	"57600":   unix.B57600,
	"115200":  unix.B115200,
	"230400":  unix.B230400,
	"460800":  unix.B460800,
	"921600":  unix.B921600,
	"1500000": unix.B1500000,
}

// expandIssue substitutes the classic getty backslash escapes in
// /etc/issue. Matches getty(8) manpage: \b, \d, \l, \m, \n, \o, \r,
// \s, \t, \u, \v. Unknown escapes are passed through verbatim so
// operator-authored issues (e.g. `\e[1m` ANSI) survive.
func expandIssue(issue, ttyPath string) string {
	uts := getUname()
	line := strings.TrimPrefix(ttyPath, "/dev/")
	now := time.Now()

	var b strings.Builder
	b.Grow(len(issue) + 64)
	i := 0
	for i < len(issue) {
		c := issue[i]
		if c != '\\' || i+1 >= len(issue) {
			b.WriteByte(c)
			i++
			continue
		}
		esc := issue[i+1]
		i += 2
		switch esc {
		case 'd':
			b.WriteString(now.Format("Mon Jan _2 2006"))
		case 'l':
			b.WriteString(line)
		case 'm':
			b.WriteString(uts.machine)
		case 'n':
			b.WriteString(uts.nodename)
		case 'o':
			b.WriteString(uts.domainname)
		case 'r':
			b.WriteString(uts.release)
		case 's':
			b.WriteString(uts.sysname)
		case 't':
			b.WriteString(now.Format("15:04:05"))
		case 'u', 'U':
			// User count — we don't track utmp here; print 0
			// rather than call utmp libraries.
			b.WriteString("0")
		case 'v':
			b.WriteString(uts.version)
		case 'b':
			// Baud rate on the terminal. Slinit doesn't track it
			// here; print empty rather than lie.
		default:
			b.WriteByte('\\')
			b.WriteByte(esc)
		}
	}
	return b.String()
}

// utsFields packs the subset of uname(2) fields /etc/issue needs.
type utsFields struct {
	sysname, nodename, release, version, machine, domainname string
}

func getUname() utsFields {
	var u unix.Utsname
	_ = unix.Uname(&u)
	return utsFields{
		sysname:    unixString(u.Sysname[:]),
		nodename:   unixString(u.Nodename[:]),
		release:    unixString(u.Release[:]),
		version:    unixString(u.Version[:]),
		machine:    unixString(u.Machine[:]),
		domainname: unixString(u.Domainname[:]),
	}
}

// unixString trims a C-string byte array to its first NUL and
// returns a Go string. u.Sysname etc. are fixed-size arrays.
func unixString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// readLoginName prints "<host> login: " and reads a username line.
// Empty entries loop back to re-print the prompt so a stray Enter
// doesn't feed an empty string to /bin/login. finit-parity for
// its restart-on-blank-line behaviour.
func readLoginName() (string, error) {
	uts := getUname()
	prompt := uts.nodename + " login: "
	r := bufio.NewReaderSize(os.Stdin, 256)
	for {
		fmt.Print(prompt)
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		name := strings.TrimSpace(line)
		if len(name) == 0 {
			continue
		}
		if len(name) > loginNameMax {
			name = name[:loginNameMax]
		}
		return name, nil
	}
}

// firstExisting returns the first path in `candidates` that is a
// regular file with any exec bit set; "" when none qualifies.
func firstExisting(candidates []string) string {
	for _, p := range candidates {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if !st.Mode().IsRegular() {
			continue
		}
		if st.Mode()&0o111 == 0 {
			continue
		}
		return p
	}
	return ""
}
