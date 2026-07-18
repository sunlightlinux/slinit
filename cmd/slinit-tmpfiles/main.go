// slinit-tmpfiles applies systemd-tmpfiles.d(5) directives at boot.
// Reads /usr/lib/tmpfiles.d/*.conf, /etc/tmpfiles.d/*.conf, and
// /run/tmpfiles.d/*.conf; per-filename overrides win (later dirs
// override earlier). Only a subset of the systemd directive set is
// implemented — the ones actually used to bootstrap /run and /var
// on real distros:
//
//	f  create a file if missing (chmod, chown)
//	F  create/truncate a file
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
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

var defaultDirs = []string{
	"/usr/lib/tmpfiles.d",
	"/etc/tmpfiles.d",
	"/run/tmpfiles.d",
}

type entry struct {
	kind  string // one-char type
	path  string
	mode  uint32
	uid   int
	gid   int
	arg   string
	force bool // '!' modifier (only-at-boot); we treat as always-apply
}

func main() {
	var dirsFlag string
	flag.StringVar(&dirsFlag, "dirs", "",
		"comma-separated tmpfiles.d directories (defaults to /usr/lib+/etc+/run/tmpfiles.d)")
	dryRun := flag.Bool("dry-run", false, "print actions without applying them")
	flag.Parse()

	dirs := defaultDirs
	if dirsFlag != "" {
		dirs = strings.Split(dirsFlag, ",")
	}

	// Collect files by basename so later dirs (/etc, /run) override
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
	if len(fields) > 2 && fields[2] != "-" {
		m, err := strconv.ParseUint(fields[2], 8, 32)
		if err != nil {
			return entry{}, fmt.Errorf("mode %q: %w", fields[2], err)
		}
		e.mode = uint32(m)
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
	return e, nil
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

func applyFile(e entry, force bool) error {
	flag := os.O_CREATE | os.O_WRONLY
	if force {
		flag |= os.O_TRUNC
	} else {
		flag |= os.O_EXCL
	}
	f, err := os.OpenFile(e.path, flag, os.FileMode(e.mode))
	if err != nil {
		if !force && os.IsExist(err) {
			return applyChown(e, false)
		}
		return err
	}
	f.Close()
	return os.Chown(e.path, e.uid, e.gid)
}

func applyDir(e entry, wipe bool) error {
	if wipe {
		os.RemoveAll(e.path)
	}
	if err := os.MkdirAll(e.path, os.FileMode(e.mode)); err != nil {
		return err
	}
	if err := os.Chmod(e.path, os.FileMode(e.mode)); err != nil {
		return err
	}
	return os.Chown(e.path, e.uid, e.gid)
}

func applyLink(e entry) error {
	if e.arg == "" {
		return fmt.Errorf("L: missing target Argument")
	}
	if _, err := os.Lstat(e.path); err == nil {
		return nil // already exists, don't clobber
	}
	return os.Symlink(e.arg, e.path)
}

func applyWrite(e entry) error {
	if e.arg == "" {
		return fmt.Errorf("w: missing value Argument")
	}
	return os.WriteFile(e.path, []byte(e.arg), 0644)
}

func applyRemove(e entry, recursive bool) error {
	if recursive {
		return os.RemoveAll(e.path)
	}
	err := os.Remove(e.path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func applyChown(e entry, recursive bool) error {
	fi, err := os.Lstat(e.path)
	if err != nil {
		return err
	}
	if err := os.Chmod(e.path, os.FileMode(e.mode)); err != nil {
		return err
	}
	if err := os.Chown(e.path, e.uid, e.gid); err != nil {
		return err
	}
	if !recursive || !fi.IsDir() {
		return nil
	}
	return filepath.Walk(e.path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if p == e.path {
			return nil
		}
		if err := os.Chmod(p, os.FileMode(e.mode)); err != nil {
			return err
		}
		return os.Chown(p, e.uid, e.gid)
	})
}
