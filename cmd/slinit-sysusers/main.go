// slinit-sysusers applies systemd-sysusers.d(5) directives at boot.
// Reads /usr/lib/sysusers.d/*.conf, /etc/sysusers.d/*.conf, and
// /run/sysusers.d/*.conf; per-filename overrides win (later dirs
// override earlier).
//
// Implemented directives:
//
//	u Name ID GECOS HomeDir Shell   create a user
//	g Name GID                       create a group
//	m Name Group                     add user to group
//	r -    Range                     range reservation (informational, no-op)
//
// Delegates the actual work to shadow-utils (useradd / groupadd /
// gpasswd) so /etc/passwd + /etc/group + /etc/shadow locking is
// handled correctly. Errors when those tools are absent — matches
// what the systemd unit would experience on a minimal image.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

var defaultDirs = []string{
	"/usr/lib/sysusers.d",
	"/etc/sysusers.d",
	"/run/sysusers.d",
}

type entry struct {
	kind   string
	name   string
	idOrGid string
	gecos   string
	home    string
	shell   string
	arg     string // for m: group name; for r: range
}

func main() {
	var dirsFlag string
	flag.StringVar(&dirsFlag, "dirs", "",
		"comma-separated sysusers.d dirs (defaults to /usr/lib+/etc+/run/sysusers.d)")
	dryRun := flag.Bool("dry-run", false, "print actions without executing them")
	flag.Parse()

	dirs := defaultDirs
	if dirsFlag != "" {
		dirs = strings.Split(dirsFlag, ",")
	}

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
			fmt.Fprintf(os.Stderr, "slinit-sysusers: parse %s: %v\n", file, err)
			failed++
			continue
		}
		for _, e := range entries {
			if *dryRun {
				fmt.Printf("would %s %s\n", e.kind, e.name)
				continue
			}
			if err := apply(e); err != nil {
				fmt.Fprintf(os.Stderr, "slinit-sysusers: %s %s: %v\n", e.kind, e.name, err)
				failed++
				continue
			}
			applied++
		}
	}
	if !*dryRun {
		fmt.Fprintf(os.Stderr, "slinit-sysusers: %d applied, %d failed\n", applied, failed)
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
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
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

func parseLine(line string) (entry, error) {
	fields := splitFields(line)
	if len(fields) < 2 {
		return entry{}, fmt.Errorf("need at least Type and Name, got %q", line)
	}
	e := entry{kind: fields[0], name: fields[1]}
	if len(fields) > 2 && fields[2] != "-" {
		e.idOrGid = fields[2]
	}
	if len(fields) > 3 && fields[3] != "-" {
		e.gecos = fields[3]
	}
	if len(fields) > 4 && fields[4] != "-" {
		e.home = fields[4]
	}
	if len(fields) > 5 && fields[5] != "-" {
		e.shell = fields[5]
	}
	// For `m` the second field is the group name; parseLine already
	// captured it as e.idOrGid. Copy across for clarity.
	if e.kind == "m" {
		e.arg = e.idOrGid
	}
	if e.kind == "r" {
		e.arg = e.idOrGid // range like "500-800"
	}
	return e, nil
}

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

func apply(e entry) error {
	switch e.kind {
	case "u":
		return applyUser(e)
	case "g":
		return applyGroup(e)
	case "m":
		return applyMembership(e)
	case "r":
		// UID/GID range reservation is informational — real allocators
		// (useradd -F, systemd-userdb) honour the range, but shadow-utils
		// pick from FIRST/LAST_SYSTEM_UID in /etc/login.defs which the
		// operator sets separately. No-op here.
		return nil
	default:
		return fmt.Errorf("unsupported type %q", e.kind)
	}
}

func applyUser(e entry) error {
	if userExists(e.name) {
		return nil
	}
	if _, err := exec.LookPath("useradd"); err != nil {
		return fmt.Errorf("useradd(8) not in PATH — shadow-utils required")
	}
	args := []string{"--system"}
	if e.idOrGid != "" {
		args = append(args, "--uid", e.idOrGid)
	}
	if e.gecos != "" {
		args = append(args, "--comment", strings.Trim(e.gecos, `"`))
	}
	if e.home != "" {
		args = append(args, "--home-dir", e.home)
	} else {
		args = append(args, "--no-create-home")
	}
	if e.shell != "" {
		args = append(args, "--shell", e.shell)
	} else {
		args = append(args, "--shell", "/sbin/nologin")
	}
	args = append(args, e.name)
	return runCmd("useradd", args...)
}

func applyGroup(e entry) error {
	if groupExists(e.name) {
		return nil
	}
	if _, err := exec.LookPath("groupadd"); err != nil {
		return fmt.Errorf("groupadd(8) not in PATH — shadow-utils required")
	}
	args := []string{"--system"}
	if e.idOrGid != "" {
		args = append(args, "--gid", e.idOrGid)
	}
	args = append(args, e.name)
	return runCmd("groupadd", args...)
}

func applyMembership(e entry) error {
	if e.arg == "" {
		return fmt.Errorf("m: missing group name")
	}
	if _, err := exec.LookPath("gpasswd"); err != nil {
		return fmt.Errorf("gpasswd(8) not in PATH — shadow-utils required")
	}
	return runCmd("gpasswd", "-a", e.name, e.arg)
}

func runCmd(bin string, args ...string) error {
	cmd := exec.Command(bin, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func userExists(name string) bool {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if idx := strings.IndexByte(sc.Text(), ':'); idx > 0 && sc.Text()[:idx] == name {
			return true
		}
	}
	return false
}

func groupExists(name string) bool {
	f, err := os.Open("/etc/group")
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if idx := strings.IndexByte(sc.Text(), ':'); idx > 0 && sc.Text()[:idx] == name {
			return true
		}
	}
	return false
}
