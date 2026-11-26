// slinit-tmpfiles applies systemd-tmpfiles.d(5) directives at boot.
// Reads /usr/lib/tmpfiles.d/*.conf, /run/tmpfiles.d/*.conf, and
// /etc/tmpfiles.d/*.conf; for the same file name /etc overrides /run
// overrides /usr/lib (systemd precedence). Only a subset of the systemd directive set is
// implemented — the ones actually used to bootstrap /run and /var
// on real distros:
//
//	f  create a file if missing (chmod, chown), writing Argument
//	F  create/truncate a file, writing Argument
//	d  create a directory
//	D  create a directory, wipe contents
//	L  create a symlink (respects Argument as target)
//	w  write a value to a file (sysctl-style; overwrites)
//	r  remove a file if it exists
//	R  remove a directory tree if it exists
//	z  chown/chmod existing path (non-recursive)
//	Z  chown/chmod existing tree (recursive)
//
// Not implemented: age-based cleanup (--clean pass), path specifiers
// (%h, %m, %U, ...), glob patterns, xattrs, ACLs, subvolumes, and the
// C/p/x/e/t types. These are the tail of systemd-tmpfiles usage and
// are additive when a real use case shows up.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/sunlightlinux/slinit/internal/safepath"
	"golang.org/x/sys/unix"
)

// defaultDirs is in ascending precedence: for the same file name a
// later directory overrides an earlier one (/etc > /run > /usr/lib).
var defaultDirs = []string{
	"/usr/lib/tmpfiles.d",
	"/run/tmpfiles.d",
	"/etc/tmpfiles.d",
}

type entry struct {
	kind  string // one-char type
	path  string
	mode  uint32
	uid   int
	gid   int
	arg   string
	force bool // '!' modifier (only-at-boot); we treat as always-apply
	// keepMode: z/Z with no Mode — leave the mode as it is. uid and gid
	// say the same with -1.
	keepMode bool
}

// version is set at build time with -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	// --version, alone: checked before anything else runs, so no other
	// argument changes meaning. The value is stamped by the package
	// build (-X main.version=...).
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Printf("slinit-tmpfiles version %s\n", version)
		return
	}
	var dirsFlag string
	flag.StringVar(&dirsFlag, "dirs", "",
		"comma-separated tmpfiles.d directories, lowest precedence first (defaults to /usr/lib+/run+/etc/tmpfiles.d)")
	dryRun := flag.Bool("dry-run", false, "print actions without applying them")
	flag.Parse()

	dirs := defaultDirs
	if dirsFlag != "" {
		dirs = strings.Split(dirsFlag, ",")
	}

	// Collect files by basename so later dirs (/run, then /etc) override
	// earlier ones (/usr/lib). Matches systemd-tmpfiles precedence.
	confs := collect(dirs)
	names := make([]string, 0, len(confs))
	for n := range confs {
		names = append(names, n)
	}
	sort.Strings(names)

	var applied, failed int
	for _, name := range names {
		file := confs[name]
		entries, err := parseFile(file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "slinit-tmpfiles: parse %s: %v\n", file, err)
			failed++
			continue
		}
		for _, e := range entries {
			if *dryRun {
				fmt.Printf("would %s %s\n", e.kind, e.path)
				continue
			}
			if err := apply(e); err != nil {
				fmt.Fprintf(os.Stderr, "slinit-tmpfiles: %s %s: %v\n", e.kind, e.path, err)
				failed++
				continue
			}
			applied++
		}
	}
	if !*dryRun {
		fmt.Fprintf(os.Stderr, "slinit-tmpfiles: %d applied, %d failed\n", applied, failed)
	}
	if failed > 0 {
		os.Exit(1)
	}
}

func collect(dirs []string) map[string]string {
	out := make(map[string]string)
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if !strings.HasSuffix(e.Name(), ".conf") {
				continue
			}
			out[e.Name()] = filepath.Join(d, e.Name())
		}
	}
	return out
}

func parseFile(path string) ([]entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []entry
	sc := bufio.NewScanner(f)
	lineno := 0
	for sc.Scan() {
		lineno++
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		e, err := parseLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineno, err)
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// parseLine splits on whitespace, honouring '-' as "default/skip".
// Columns: Type Path Mode UID GID Age Argument
func parseLine(line string) (entry, error) {
	fields := splitFields(line)
	if len(fields) < 2 {
		return entry{}, fmt.Errorf("need at least Type and Path, got %q", line)
	}
	e := entry{
		mode: 0644,
		uid:  0,
		gid:  0,
	}
	e.kind = fields[0]
	// '!' or '+' modifiers on the kind — we ignore semantics (treat
	// as always-apply) but strip so the switch below matches.
	e.kind = strings.TrimLeft(e.kind, "!+=-")
	if e.kind == "" {
		return entry{}, fmt.Errorf("empty type after modifier trim")
	}
	e.path = fields[1]
	// tmpfiles.d(5): a missing or "-" Mode is 0755 for a directory, and
	// for z/Z a missing or "-" Mode, User or Group leaves that alone.
	switch e.kind {
	case "d", "D":
		e.mode = 0755
	case "z", "Z":
		e.keepMode, e.uid, e.gid = true, -1, -1
	}
	if len(fields) > 2 && fields[2] != "-" {
		m, err := strconv.ParseUint(fields[2], 8, 32)
		if err != nil {
			return entry{}, fmt.Errorf("mode %q: %w", fields[2], err)
		}
		e.mode = uint32(m)
		e.keepMode = false
	}
	if len(fields) > 3 && fields[3] != "-" {
		u, err := lookupUID(fields[3])
		if err != nil {
			return entry{}, err
		}
		e.uid = u
	}
	if len(fields) > 4 && fields[4] != "-" {
		g, err := lookupGID(fields[4])
		if err != nil {
			return entry{}, err
		}
		e.gid = g
	}
	// fields[5] = Age (skipped in MVP).
	if len(fields) > 6 {
		e.arg = strings.Join(fields[6:], " ")
	}
	if e.kind == "f" || e.kind == "F" || e.kind == "w" {
		if e.arg == "-" { // "-" means no argument, as in every column
			e.arg = ""
		}
		arg, err := cunescape(e.arg)
		if err != nil {
			return entry{}, fmt.Errorf("argument %q: %w", e.arg, err)
		}
		e.arg = arg
	}
	return e, nil
}

// cunescape resolves the C-style escapes tmpfiles.d(5) allows in the
// Argument column: \a \b \f \n \r \t \v \\ \" \' \s (space),
// \xNN (hex) and \NNN (octal).
func cunescape(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(s) {
			return "", fmt.Errorf("trailing backslash")
		}
		switch c = s[i]; c {
		case 'a':
			b.WriteByte('\a')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte('\v')
		case 's':
			b.WriteByte(' ')
		case '\\', '"', '\'':
			b.WriteByte(c)
		case 'x':
			if i+3 > len(s) {
				return "", fmt.Errorf("short \\x escape")
			}
			n, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
			if err != nil {
				return "", fmt.Errorf("bad \\x escape %q", s[i-1:i+3])
			}
			b.WriteByte(byte(n))
			i += 2
		case '0', '1', '2', '3', '4', '5', '6', '7':
			if i+3 > len(s) {
				return "", fmt.Errorf("short octal escape")
			}
			n, err := strconv.ParseUint(s[i:i+3], 8, 8)
			if err != nil {
				return "", fmt.Errorf("bad octal escape %q", s[i-1:i+3])
			}
			b.WriteByte(byte(n))
			i += 2
		default:
			return "", fmt.Errorf("unknown escape \\%c", c)
		}
	}
	return b.String(), nil
}

// splitFields is a whitespace splitter that respects double-quoted
// spans so the Argument column can contain spaces (common in `w`
// entries with a value).
func splitFields(line string) []string {
	var out []string
	var buf strings.Builder
	inQuote := false
	for _, r := range line {
		switch {
		case r == '"':
			inQuote = !inQuote
		case (r == ' ' || r == '\t') && !inQuote:
			if buf.Len() > 0 {
				out = append(out, buf.String())
				buf.Reset()
			}
		default:
			buf.WriteRune(r)
		}
	}
	if buf.Len() > 0 {
		out = append(out, buf.String())
	}
	return out
}

func lookupUID(s string) (int, error) {
	if n, err := strconv.Atoi(s); err == nil {
		return n, nil
	}
	// Fall back to /etc/passwd lookup (avoid os/user cgo dep at PID 1).
	return lookupPasswdField("/etc/passwd", s, 2)
}

func lookupGID(s string) (int, error) {
	if n, err := strconv.Atoi(s); err == nil {
		return n, nil
	}
	return lookupPasswdField("/etc/group", s, 2)
}

// lookupPasswdField parses /etc/passwd or /etc/group and returns the
// numeric field at `idx` for the entry whose first field equals `name`.
func lookupPasswdField(file, name string, idx int) (int, error) {
	f, err := os.Open(file)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Split(sc.Text(), ":")
		if len(parts) > idx && parts[0] == name {
			n, err := strconv.Atoi(parts[idx])
			if err != nil {
				return 0, fmt.Errorf("%s: bad numeric in %s field %d", file, name, idx)
			}
			return n, nil
		}
	}
	return 0, fmt.Errorf("%s: user/group %q not found", file, name)
}

func apply(e entry) error {
	switch e.kind {
	case "f":
		return applyFile(e, false)
	case "F":
		return applyFile(e, true)
	case "d":
		return applyDir(e, false)
	case "D":
		return applyDir(e, true)
	case "L":
		return applyLink(e)
	case "w":
		return applyWrite(e)
	case "r":
		return applyRemove(e, false)
	case "R":
		return applyRemove(e, true)
	case "z":
		return applyChown(e, false)
	case "Z":
		return applyChown(e, true)
	default:
		return fmt.Errorf("unsupported type %q", e.kind)
	}
}

// Every type below resolves the entry's parent with safepath, which
// follows a symlinked parent only when that cannot carry root out of what
// the link's owner owns (CHASE_SAFE, see internal/safepath), and then
// works relative to the parent's descriptor, never by path. Resolving the
// whole path let a link anywhere in it redirect root: d base/link/new
// created outside/new, z re-moded and r deleted outside/victim.

func applyFile(e entry, force bool) error {
	pfd, name, err := safepath.OpenParent(e.path)
	if err != nil {
		return err
	}
	defer unix.Close(pfd)
	// O_NOFOLLOW because F has no O_EXCL to refuse a symlink for us,
	// and it does truncate: without it, an F naming a symlink opened
	// and emptied whatever the link pointed at, as root, at boot.
	// O_EXCL covers the plain f — a symlink there fails with EEXIST and
	// falls through to applyChown, which skips it.
	//
	// O_NONBLOCK because an F naming a FIFO blocked in open(2) waiting
	// for a reader, and hung the boot. The type is checked on the
	// descriptor before anything is written, and the truncate waits for
	// that check: O_TRUNC in the open would have run first.
	flag := unix.O_CREAT | unix.O_WRONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_NOCTTY | unix.O_CLOEXEC
	if !force {
		flag |= unix.O_EXCL
	}
	fd, err := unix.Openat(pfd, name, flag, e.mode&0o777)
	if err != nil {
		if !force && err == unix.EEXIST {
			return applyChown(e, false)
		}
		return &os.PathError{Op: "open", Path: e.path, Err: err}
	}
	f, st, err := regularFile(fd, e.path)
	if err != nil {
		return err
	}
	if err := checkHardlink(st, e.path); err != nil {
		f.Close()
		return err
	}
	if force {
		if err := f.Truncate(0); err != nil {
			f.Close()
			return err
		}
	}
	if e.arg != "" {
		if _, err := f.WriteString(e.arg); err != nil {
			f.Close()
			return err
		}
	}
	// Owner and mode through the descriptor, not the path, which may
	// name something else by now. The create mode above went through
	// the umask and an F on an existing file kept its old mode, so set
	// the mode explicitly — raw, after the chown (see applyDir and
	// chownChmod).
	if err := f.Chown(e.uid, e.gid); err != nil {
		f.Close()
		return err
	}
	if err := syscall.Fchmod(int(f.Fd()), e.mode); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// regularFile takes fd, opened O_NONBLOCK, and returns it as an *os.File
// with O_NONBLOCK cleared, or closes it and fails if it is not a regular
// file. A FIFO, device or socket is refused rather than written to.
func regularFile(fd int, path string) (*os.File, *unix.Stat_t, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, nil, &os.PathError{Op: "stat", Path: path, Err: err}
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		unix.Close(fd)
		return nil, nil, fmt.Errorf("%s: not a regular file", path)
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		unix.Close(fd)
		return nil, nil, err
	}
	return os.NewFile(uintptr(fd), path), &st, nil
}

// checkHardlink refuses to change a non-directory with more than one
// link while fs.protected_hardlinks is off, as systemd-tmpfiles does
// (hardlink_vulnerable): a user could otherwise link /etc/shadow into a
// directory this tool fixes up and have root re-own or empty it.
func checkHardlink(st *unix.Stat_t, path string) error {
	if st.Mode&unix.S_IFMT != unix.S_IFDIR && st.Nlink > 1 && !safepath.HardlinksProtected() {
		return fmt.Errorf("%s: refusing to change a hard-linked file while fs.protected_hardlinks is off", path)
	}
	return nil
}

func applyDir(e entry, wipe bool) error {
	pfd, name, err := safepath.MkdirParent(e.path, e.mode&0o777)
	if err != nil {
		return err
	}
	defer unix.Close(pfd)
	if wipe {
		removeAt(pfd, name, true)
		if afterWipeHook != nil {
			afterWipeHook()
		}
	}
	if err := unix.Mkdirat(pfd, name, e.mode&0o777); err != nil && err != unix.EEXIST {
		return &os.PathError{Op: "mkdir", Path: e.path, Err: err}
	}
	// An existing entry satisfies the mkdir, and a chmod or chown by
	// path follows a symlink: a link planted at a d path in /tmp (or
	// between D's wipe and the mkdir) had root re-mode and re-own its
	// target. Open the final component without following it and change
	// the directory through the descriptor; a symlink there is refused,
	// as systemd-tmpfiles refuses it.
	fd, err := unix.Openat(pfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		var st unix.Stat_t
		if unix.Fstatat(pfd, name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil && st.Mode&unix.S_IFMT == unix.S_IFLNK {
			return fmt.Errorf("refusing to follow a symlink")
		}
		return &os.PathError{Op: "open", Path: e.path, Err: err}
	}
	defer syscall.Close(fd)
	// The mode goes in raw: os.FileMode(e.mode) dropped 07000, which Go
	// keeps as os.ModeSticky and friends, so a 1777 directory lost its
	// sticky bit.
	if err := syscall.Fchown(fd, e.uid, e.gid); err != nil {
		return err
	}
	return syscall.Fchmod(fd, e.mode)
}

// afterWipeHook, when set, runs between D's wipe and its mkdir: tests
// use it to plant a symlink in exactly that window.
var afterWipeHook func()

func applyLink(e entry) error {
	if e.arg == "" {
		return fmt.Errorf("L: missing target Argument")
	}
	pfd, name, err := safepath.OpenParent(e.path)
	if err != nil {
		return err
	}
	defer unix.Close(pfd)
	if err := unix.Symlinkat(e.arg, pfd, name); err != nil && err != unix.EEXIST {
		return &os.PathError{Op: "symlink", Path: e.path, Err: err}
	}
	return nil // an existing entry is left alone, not clobbered
}

func applyWrite(e entry) error {
	if e.arg == "" {
		return fmt.Errorf("w: missing value Argument")
	}
	pfd, name, err := safepath.OpenParent(e.path)
	if err != nil {
		return err
	}
	defer unix.Close(pfd)
	// No O_CREAT: w writes into a file that exists, and skips a missing
	// one, as systemd-tmpfiles does ("Not writing missing file"). It
	// used to create it. O_NOFOLLOW refuses a symlink at the final
	// component; symlinked directories on the way, such as
	// /sys/class/backlight/*, are root's and still followed. O_NONBLOCK
	// keeps a FIFO from blocking the open forever; anything but a
	// regular file is then refused (sysfs and procfs knobs are regular
	// files). O_TRUNC does nothing to a FIFO or device.
	fd, err := unix.Openat(pfd, name, unix.O_WRONLY|unix.O_TRUNC|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err == unix.ENOENT {
		return nil
	}
	if err != nil {
		return &os.PathError{Op: "open", Path: e.path, Err: err}
	}
	f, _, err := regularFile(fd, e.path)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(e.arg); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func applyRemove(e entry, recursive bool) error {
	pfd, name, err := safepath.OpenParent(e.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(pfd)
	if err := removeAt(pfd, name, recursive); err != nil && !errors.Is(err, os.ErrNotExist) {
		return &os.PathError{Op: "remove", Path: e.path, Err: err}
	}
	return nil
}

// removeAt removes name from the directory dirfd: an R tree through an
// os.Root on that directory, which removes a symlink and never what it
// names, and an r entry with unlinkat, as os.Remove did by path.
func removeAt(dirfd int, name string, recursive bool) error {
	if recursive {
		root, err := os.OpenRoot("/proc/self/fd/" + strconv.Itoa(dirfd))
		if err != nil {
			return err
		}
		defer root.Close()
		return root.RemoveAll(name)
	}
	err := unix.Unlinkat(dirfd, name, 0)
	if err == unix.EISDIR {
		err = unix.Unlinkat(dirfd, name, unix.AT_REMOVEDIR)
	}
	return err
}

// applyChown implements z (this path) and Z (this path, recursively).
//
// Symlinks are skipped rather than followed. There is nothing worth
// setting on a symlink itself on Linux — the mode is ignored and the
// ownership only governs removal from a sticky directory — so skipping
// is both the safe and the correct reading.
//
// The walk goes by descriptor: each entry is opened O_PATH|O_NOFOLLOW
// relative to its parent's descriptor and changed through that. It used
// to Lstat each path (filepath.Walk) and then chown/chmod it by path, so
// whoever owned the tree could swap a subdirectory for a symlink to /etc
// between the check and the change, and root changed /etc (the
// CVE-2017-18078 class). A swap now meets an O_NOFOLLOW open and is
// skipped as a symlink. finit moved this work onto an fd in 9bfbdc6b.
func applyChown(e entry, recursive bool) error {
	pfd, name, err := safepath.OpenParent(e.path)
	if err != nil {
		return err
	}
	defer unix.Close(pfd)
	return chownAt(pfd, name, e.path, e, recursive)
}

// chownAt applies e to name in dirfd and, if recursive, below it. A
// refused entry (a hard link) does not stop the rest of the tree; the
// first error is returned.
func chownAt(dirfd int, name, path string, e entry, recursive bool) error {
	fd, err := unix.Openat(dirfd, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return &os.PathError{Op: "stat", Path: path, Err: err}
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		return nil
	}
	first := chownChmod(fd, &st, path, e)
	if !recursive || st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return first
	}
	dfd, err := unix.Openat(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: path, Err: err}
	}
	dir := os.NewFile(uintptr(dfd), path)
	names, err := dir.Readdirnames(-1)
	dir.Close()
	if err != nil {
		return err
	}
	for _, n := range names {
		if err := chownAt(fd, n, filepath.Join(path, n), e, true); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// chownChmod gives the inode open as fd (O_PATH) e's owner, then e's
// mode, skipping whichever e leaves alone. It goes through
// /proc/self/fd/N, which names that inode whatever its path names now.
// Owner before mode, and no chown at all for -1/-1: chown(2) clears
// setuid/setgid on a regular file, even with -1/-1. The mode goes in raw
// (see applyDir).
func chownChmod(fd int, st *unix.Stat_t, path string, e entry) error {
	if err := checkHardlink(st, path); err != nil {
		return err
	}
	inode := "/proc/self/fd/" + strconv.Itoa(fd)
	if e.uid >= 0 || e.gid >= 0 {
		if err := os.Chown(inode, e.uid, e.gid); err != nil {
			return fmt.Errorf("%s: chown: %w", path, err)
		}
	}
	if e.keepMode {
		return nil
	}
	if err := syscall.Chmod(inode, e.mode); err != nil {
		return fmt.Errorf("%s: chmod: %w", path, err)
	}
	return nil
}
