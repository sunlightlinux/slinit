// slinit-sysusers applies systemd-sysusers.d(5) directives at boot.
// Reads /usr/lib/sysusers.d/*.conf, /run/sysusers.d/*.conf, and
// /etc/sysusers.d/*.conf; for the same file name /etc overrides /run
// overrides /usr/lib (systemd precedence).
//
// Implemented directives:
//
//	u Name ID GECOS HomeDir Shell   create a user (ID may be UID:GID)
//	g Name GID                       create a group
//	m Name Group                     add user to group
//	r -    Range                     range reservation (informational, no-op)
//
// As systemd-sysusers does, every file is read before anything is
// created; then all groups are created, then all users, then all
// memberships, so a line may name a group or user declared later.
// An m line whose user or group nobody declares creates it.
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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// defaultDirs is in ascending precedence: for the same file name a
// later directory overrides an earlier one (/etc > /run > /usr/lib).
var defaultDirs = []string{
	"/usr/lib/sysusers.d",
	"/run/sysusers.d",
	"/etc/sysusers.d",
}

type entry struct {
	kind    string
	name    string
	idOrGid string
	gid     string // for u: GID or group name from a UID:GID ID field
	gecos   string
	home    string
	shell   string
	arg     string // for m: group name; for r: range
	src     string // file:line it came from, for messages
}

// version is set at build time with -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	// --version, alone: checked before anything else runs, so no other
	// argument changes meaning. The value is stamped by the package
	// build (-X main.version=...).
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Printf("slinit-sysusers version %s\n", version)
		return
	}
	var dirsFlag string
	flag.StringVar(&dirsFlag, "dirs", "",
		"comma-separated sysusers.d dirs, lowest precedence first (defaults to /usr/lib+/run+/etc/sysusers.d)")
	dryRun := flag.Bool("dry-run", false, "print actions without executing them")
	flag.Parse()

	dirs := defaultDirs
	if dirsFlag != "" {
		dirs = strings.Split(dirsFlag, ",")
	}
	os.Exit(run(dirs, *dryRun, os.Stdout, os.Stderr))
}

// run reads every file first and only then touches the account
// database, in the order systemd-sysusers uses: all groups, then all
// users, then all memberships. A line may therefore refer to a group
// or user declared later in the same file or in a later file.
func run(dirs []string, dryRun bool, stdout, stderr io.Writer) int {
	confs := collect(dirs)
	names := make([]string, 0, len(confs))
	for n := range confs {
		names = append(names, n)
	}
	sort.Strings(names)

	var applied, failed int
	p := newPlan()
	for _, name := range names {
		file := confs[name]
		entries, err := parseFile(file)
		if err != nil {
			fmt.Fprintf(stderr, "slinit-sysusers: parse %s: %v\n", file, err)
			failed++
			continue
		}
		for _, e := range entries {
			p.add(e, stderr)
		}
	}
	p.addImplicit()

	for _, e := range p.ordered() {
		if dryRun {
			fmt.Fprintf(stdout, "would %s %s\n", e.kind, e.name)
			continue
		}
		if err := apply(e); err != nil {
			fmt.Fprintf(stderr, "slinit-sysusers: %s %s: %v\n", e.kind, e.name, err)
			failed++
			continue
		}
		applied++
	}
	if !dryRun {
		fmt.Fprintf(stderr, "slinit-sysusers: %d applied, %d failed\n", applied, failed)
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// plan is the merged content of every file, split by directive type
// as systemd-sysusers keeps it: one user and one group per name (the
// first declaration wins), one membership per user/group pair.
type plan struct {
	ranges, groups, users, members, other []entry
	groupIdx, userIdx                     map[string]int
	memberSeen                            map[[2]string]bool
}

func newPlan() *plan {
	return &plan{
		groupIdx:   make(map[string]int),
		userIdx:    make(map[string]int),
		memberSeen: make(map[[2]string]bool),
	}
}

func (p *plan) add(e entry, warn io.Writer) {
	switch e.kind {
	case "u", "g":
		list, idx, what := &p.users, p.userIdx, "user"
		if e.kind == "g" {
			list, idx, what = &p.groups, p.groupIdx, "group"
		}
		if i, ok := idx[e.name]; ok {
			// Identical lines are fine; a different one loses to the
			// first, with a warning (systemd-sysusers does the same).
			if prev := (*list)[i]; !sameEntry(prev, e) {
				fmt.Fprintf(warn, "slinit-sysusers: %s: conflict with earlier configuration for %s '%s' in %s, ignoring line\n",
					e.src, what, e.name, prev.src)
			}
			return
		}
		idx[e.name] = len(*list)
		*list = append(*list, e)
	case "m":
		key := [2]string{e.name, e.arg}
		if p.memberSeen[key] {
			return
		}
		p.memberSeen[key] = true
		p.members = append(p.members, e)
	case "r":
		p.ranges = append(p.ranges, e)
	default:
		p.other = append(p.other, e)
	}
}

// sameEntry compares two declarations ignoring where they came from.
func sameEntry(a, b entry) bool {
	a.src, b.src = "", ""
	return a == b
}

// addImplicit declares the users and groups that m lines name but no
// u or g line does, as systemd-sysusers' add_implicit() does. A group
// named by a u line is not added: useradd creates it with the user.
func (p *plan) addImplicit() {
	for _, m := range p.members {
		if _, ok := p.userIdx[m.name]; !ok {
			p.add(entry{kind: "u", name: m.name, src: m.src}, io.Discard)
		}
		if m.arg == "" {
			continue
		}
		_, isUser := p.userIdx[m.arg]
		_, isGroup := p.groupIdx[m.arg]
		if !isUser && !isGroup {
			p.add(entry{kind: "g", name: m.arg, src: m.src}, io.Discard)
		}
	}
}

// ordered returns the entries in application order: ranges, groups,
// users, memberships, then lines of an unknown type (which fail).
// A user whose primary group is another user's group (u foo -:bar
// with u bar but no g bar) comes after that user, whose useradd
// creates the group.
func (p *plan) ordered() []entry {
	out := make([]entry, 0, len(p.ranges)+len(p.groups)+len(p.users)+len(p.members)+len(p.other))
	out = append(out, p.ranges...)
	out = append(out, p.groups...)
	done := make([]bool, len(p.users))
	var visit func(i int)
	visit = func(i int) {
		if done[i] {
			return
		}
		done[i] = true
		u := p.users[i]
		if _, isGroup := p.groupIdx[u.gid]; !isGroup && u.gid != "" {
			if j, ok := p.userIdx[u.gid]; ok {
				visit(j)
			}
		}
		out = append(out, u)
	}
	for i := range p.users {
		visit(i)
	}
	out = append(out, p.members...)
	return append(out, p.other...)
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
		e.src = fmt.Sprintf("%s:%d", path, lineno)
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
	// sysusers.d(5): a u line's ID may be UID:GID, the GID part being
	// a number or a group name.
	if e.kind == "u" {
		if uid, gid, ok := strings.Cut(e.idOrGid, ":"); ok {
			e.idOrGid, e.gid = uid, gid
			if e.idOrGid == "-" {
				e.idOrGid = ""
			}
		}
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
	if _, err := lookPath("useradd"); err != nil {
		return fmt.Errorf("useradd(8) not in PATH — shadow-utils required")
	}
	return runCmd("useradd", userAddArgs(e, groupExists)...)
}

// userAddArgs builds useradd's argv for e. groupExists is asked
// whether a group named after the user is already there: with no
// group given, useradd --system creates one of that name and exits 9
// if it exists, where systemd-sysusers makes the user's primary group
// the existing one — so `g foo` followed by `u foo` works there.
func userAddArgs(e entry, groupExists func(string) bool) []string {
	args := []string{"--system"}
	if e.idOrGid != "" {
		args = append(args, "--uid", e.idOrGid)
	}
	if e.gid != "" {
		args = append(args, "--gid", e.gid)
	} else if groupExists(e.name) {
		args = append(args, "--gid", e.name)
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
	return args
}

func applyGroup(e entry) error {
	if groupExists(e.name) {
		return nil
	}
	if _, err := lookPath("groupadd"); err != nil {
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
	if _, err := lookPath("gpasswd"); err != nil {
		return fmt.Errorf("gpasswd(8) not in PATH — shadow-utils required")
	}
	return runCmd("gpasswd", "-a", e.name, e.arg)
}

// The account database and the shadow-utils tools, as variables so
// tests can point them at a scratch tree and a fake runner.
var (
	passwdFile = "/etc/passwd"
	groupFile  = "/etc/group"
	lookPath   = exec.LookPath
	runCmd     = func(bin string, args ...string) error {
		cmd := exec.Command(bin, args...)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
)

// nssLookup asks the name service switch (getent) whether db
// ("passwd" or "group") knows name. systemd-sysusers checks through
// NSS too: an account that lives in LDAP, sssd or userdb exists even
// though /etc/passwd does not list it, and creating a local system
// account of the same name would shadow it. A system without getent
// falls back to the files alone.
var nssLookup = func(db, name string) bool {
	bin, err := exec.LookPath("getent")
	if err != nil {
		return false
	}
	return exec.Command(bin, db, name).Run() == nil
}

func userExists(name string) bool {
	return fileHasName(passwdFile, name) || nssLookup("passwd", name)
}

func groupExists(name string) bool {
	return fileHasName(groupFile, name) || nssLookup("group", name)
}

func fileHasName(path, name string) bool {
	f, err := os.Open(path)
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
