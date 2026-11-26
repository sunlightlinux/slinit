package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLineUser(t *testing.T) {
	e, err := parseLine(`u httpd 400 "Web server user" /var/www /sbin/nologin`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if e.kind != "u" || e.name != "httpd" || e.idOrGid != "400" {
		t.Errorf("got kind=%q name=%q id=%q, want u httpd 400", e.kind, e.name, e.idOrGid)
	}
	if e.gecos != "Web server user" {
		t.Errorf("gecos: got %q, want 'Web server user'", e.gecos)
	}
	if e.home != "/var/www" || e.shell != "/sbin/nologin" {
		t.Errorf("home/shell: got %q %q", e.home, e.shell)
	}
}

func TestParseLineGroup(t *testing.T) {
	e, err := parseLine(`g wheel 10`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if e.kind != "g" || e.name != "wheel" || e.idOrGid != "10" {
		t.Errorf("got kind=%q name=%q id=%q, want g wheel 10", e.kind, e.name, e.idOrGid)
	}
}

func TestParseLineMembership(t *testing.T) {
	e, err := parseLine(`m alice wheel`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if e.kind != "m" || e.name != "alice" || e.arg != "wheel" {
		t.Errorf("got kind=%q name=%q arg=%q, want m alice wheel", e.kind, e.name, e.arg)
	}
}

func TestParseLineDefaults(t *testing.T) {
	e, err := parseLine(`u nobody - - - -`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if e.idOrGid != "" || e.gecos != "" || e.home != "" || e.shell != "" {
		t.Errorf("all-dash line must leave optional fields empty: %+v", e)
	}
}

func TestParseLineRejectsShort(t *testing.T) {
	if _, err := parseLine("u"); err == nil {
		t.Error("single-field line must be rejected")
	}
}

func TestParseLineUserUIDGID(t *testing.T) {
	for _, tc := range []struct{ in, uid, gid string }{
		{`u httpd 400:401`, "400", "401"},
		{`u httpd 400:web`, "400", "web"},
		{`u httpd 400`, "400", ""},
	} {
		e, err := parseLine(tc.in)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.in, err)
		}
		if e.idOrGid != tc.uid || e.gid != tc.gid {
			t.Errorf("parse %q: got uid=%q gid=%q, want %q %q", tc.in, e.idOrGid, e.gid, tc.uid, tc.gid)
		}
	}
	// g lines keep their ID untouched.
	if e, _ := parseLine(`g web 401`); e.idOrGid != "401" || e.gid != "" {
		t.Errorf("g line: got id=%q gid=%q", e.idOrGid, e.gid)
	}
}

func TestUserAddArgs(t *testing.T) {
	e, _ := parseLine(`u httpd 400:web "Web" /var/www /bin/sh`)
	got := strings.Join(userAddArgs(e, noGroups), " ")
	want := "--system --uid 400 --gid web --comment Web --home-dir /var/www --shell /bin/sh httpd"
	if got != want {
		t.Errorf("useradd args:\n got %q\nwant %q", got, want)
	}
	e, _ = parseLine(`u nobody -`)
	got = strings.Join(userAddArgs(e, noGroups), " ")
	want = "--system --no-create-home --shell /sbin/nologin nobody"
	if got != want {
		t.Errorf("useradd args:\n got %q\nwant %q", got, want)
	}
}

func noGroups(string) bool { return false }

// TestUserAddArgsReusesExistingGroup: `g foo` then `u foo` made
// useradd --system try to create group foo again and exit 9.
// systemd-sysusers makes the existing group the user's primary one.
func TestUserAddArgsReusesExistingGroup(t *testing.T) {
	exists := func(name string) bool { return name == "foo" }
	e, _ := parseLine(`u foo -`)
	got := strings.Join(userAddArgs(e, exists), " ")
	want := "--system --gid foo --no-create-home --shell /sbin/nologin foo"
	if got != want {
		t.Errorf("useradd args:\n got %q\nwant %q", got, want)
	}
	// An explicit group still wins.
	e, _ = parseLine(`u foo 400:web`)
	got = strings.Join(userAddArgs(e, exists), " ")
	want = "--system --uid 400 --gid web --no-create-home --shell /sbin/nologin foo"
	if got != want {
		t.Errorf("useradd args:\n got %q\nwant %q", got, want)
	}
}

// TestCollectPrecedence: for the same file name /etc beats /run beats
// /usr/lib (systemd order), and files are merged and sorted by name.
func TestCollectPrecedence(t *testing.T) {
	root := t.TempDir()
	dirs := make([]string, len(defaultDirs))
	for i, d := range defaultDirs {
		dirs[i] = filepath.Join(root, d)
		if err := os.MkdirAll(dirs[i], 0755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(dir, name string) {
		if err := os.WriteFile(filepath.Join(root, dir, name), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("/usr/lib/sysusers.d", "all.conf")
	write("/run/sysusers.d", "all.conf")
	write("/etc/sysusers.d", "all.conf")
	write("/usr/lib/sysusers.d", "run-usr.conf")
	write("/run/sysusers.d", "run-usr.conf")

	got := collect(dirs)
	if p := filepath.Join(root, "/etc/sysusers.d/all.conf"); got["all.conf"] != p {
		t.Errorf("all.conf: got %q, want %q", got["all.conf"], p)
	}
	if p := filepath.Join(root, "/run/sysusers.d/run-usr.conf"); got["run-usr.conf"] != p {
		t.Errorf("run-usr.conf: got %q, want %q", got["run-usr.conf"], p)
	}
}

// fakeShadow stands in for useradd/groupadd/gpasswd over a scratch
// passwd/group pair, refusing what the real tools refuse: a --gid or
// gpasswd group that does not exist, a gpasswd user that does not.
type fakeShadow struct {
	t    *testing.T
	argv []string
}

func (f *fakeShadow) has(file, name string) bool {
	b, err := os.ReadFile(file)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		fields := strings.Split(l, ":")
		if len(fields) >= 3 && (fields[0] == name || fields[2] == name) {
			return true
		}
	}
	return false
}

func (f *fakeShadow) appendLine(file, line string) {
	fh, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	defer fh.Close()
	if _, err := fh.WriteString(line + "\n"); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeShadow) run(bin string, args ...string) error {
	f.argv = append(f.argv, bin+" "+strings.Join(args, " "))
	name := args[len(args)-1]
	opt := func(o string) string {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == o {
				return args[i+1]
			}
		}
		return ""
	}
	switch bin {
	case "groupadd":
		id := opt("--gid")
		if id == "" {
			id = "999"
		}
		f.appendLine(groupFile, name+":x:"+id+":")
	case "useradd":
		g := opt("--gid")
		if g != "" && !f.has(groupFile, g) {
			return fmt.Errorf("useradd: group '%s' does not exist", g)
		}
		if g == "" {
			if f.has(groupFile, name) {
				return fmt.Errorf("useradd: group %s exists", name)
			}
			f.appendLine(groupFile, name+":x:998:")
		}
		f.appendLine(passwdFile, name+":x:998:998::/:/sbin/nologin")
	case "gpasswd":
		if !f.has(passwdFile, args[1]) {
			return fmt.Errorf("gpasswd: user '%s' does not exist", args[1])
		}
		if !f.has(groupFile, args[2]) {
			return fmt.Errorf("gpasswd: group '%s' does not exist", args[2])
		}
	}
	return nil
}

// runConfs writes confs (basename -> content) into a scratch
// sysusers.d, points the tool at a scratch passwd/group and a fake
// runner, and runs it. Returns the exit code, the commands run in
// order, and stderr.
func runConfs(t *testing.T, confs map[string]string) (int, []string, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "sysusers.d")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for n, c := range confs {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0644); err != nil {
			t.Fatal(err)
		}
	}
	oldP, oldG, oldL, oldR := passwdFile, groupFile, lookPath, runCmd
	t.Cleanup(func() { passwdFile, groupFile, lookPath, runCmd = oldP, oldG, oldL, oldR })
	passwdFile = filepath.Join(root, "passwd")
	groupFile = filepath.Join(root, "group")
	for _, f := range []string{passwdFile, groupFile} {
		if err := os.WriteFile(f, []byte("root:x:0:0::/root:/bin/sh\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	fs := &fakeShadow{t: t}
	lookPath = func(string) (string, error) { return "/fake", nil }
	runCmd = fs.run
	var out, errb bytes.Buffer
	rc := run([]string{dir}, false, &out, &errb)
	return rc, fs.argv, errb.String()
}

func checkCmds(t *testing.T, rc int, got []string, stderr string, want ...string) {
	t.Helper()
	if rc != 0 {
		t.Errorf("exit %d, want 0; stderr:\n%s", rc, stderr)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}
}

// TestGroupInLaterFile: `u foo -:bar` and `m foo bar` in a.conf, the
// `g bar 777` they need in b.conf. Applying in file order ran useradd
// and gpasswd before groupadd, both failed; systemd-sysusers creates
// all groups first.
func TestGroupInLaterFile(t *testing.T) {
	rc, cmds, stderr := runConfs(t, map[string]string{
		"a-foo.conf": "u svcfoo -:svcbar \"Foo daemon\"\nm svcfoo svcbar\n",
		"b-bar.conf": "g svcbar 777\n",
	})
	checkCmds(t, rc, cmds, stderr,
		"groupadd --system --gid 777 svcbar",
		"useradd --system --gid svcbar --comment Foo daemon --no-create-home --shell /sbin/nologin svcfoo",
		"gpasswd -a svcfoo svcbar")
}

// TestGroupAfterSameNameUser: `g bar 777` after `u bar` must still
// give group bar GID 777, not one useradd picks for its user group.
func TestGroupAfterSameNameUser(t *testing.T) {
	rc, cmds, stderr := runConfs(t, map[string]string{
		"x.conf": "u bar -\ng bar 777\n",
	})
	checkCmds(t, rc, cmds, stderr,
		"groupadd --system --gid 777 bar",
		"useradd --system --gid bar --no-create-home --shell /sbin/nologin bar")
}

// TestMemberImplicit: an m line whose user and group nobody declares
// creates both (systemd-sysusers add_implicit), group first.
func TestMemberImplicit(t *testing.T) {
	rc, cmds, stderr := runConfs(t, map[string]string{
		"x.conf": "m alice wheelx\n",
	})
	checkCmds(t, rc, cmds, stderr,
		"groupadd --system wheelx",
		"useradd --system --no-create-home --shell /sbin/nologin alice",
		"gpasswd -a alice wheelx")
}

// TestMemberOfUserGroup: m into a group that a u line creates (as the
// user's own group) adds no implicit g line.
func TestMemberOfUserGroup(t *testing.T) {
	rc, cmds, stderr := runConfs(t, map[string]string{
		"x.conf": "m alice svc\nu svc -\nu alice -\n",
	})
	checkCmds(t, rc, cmds, stderr,
		"useradd --system --no-create-home --shell /sbin/nologin svc",
		"useradd --system --no-create-home --shell /sbin/nologin alice",
		"gpasswd -a alice svc")
}

// TestUserGroupOfLaterUser: `u foo -:bar` with only `u bar` (later)
// declaring bar: bar's useradd creates the group, so it goes first.
func TestUserGroupOfLaterUser(t *testing.T) {
	rc, cmds, stderr := runConfs(t, map[string]string{
		"x.conf": "u foo -:bar\nu bar -\n",
	})
	checkCmds(t, rc, cmds, stderr,
		"useradd --system --no-create-home --shell /sbin/nologin bar",
		"useradd --system --gid bar --no-create-home --shell /sbin/nologin foo")
}

// TestDuplicateFirstWins: a second, different declaration of the same
// group is ignored with a warning; an identical one silently. Neither
// fails the run.
func TestDuplicateFirstWins(t *testing.T) {
	rc, cmds, stderr := runConfs(t, map[string]string{
		"a.conf": "g grp 100\n",
		"b.conf": "g grp 100\ng grp 200\nm u1 grp\nm u1 grp\n",
	})
	checkCmds(t, rc, cmds, stderr,
		"groupadd --system --gid 100 grp",
		"useradd --system --no-create-home --shell /sbin/nologin u1",
		"gpasswd -a u1 grp")
	if n := strings.Count(stderr, "conflict with earlier configuration for group 'grp'"); n != 1 {
		t.Errorf("want one conflict warning, got %d:\n%s", n, stderr)
	}
	if !strings.Contains(stderr, "b.conf:2") {
		t.Errorf("warning should name b.conf:2:\n%s", stderr)
	}
}

// TestFailuresStillCounted: an unknown type and a failing command
// still give exit 1 while the rest is applied.
func TestFailuresStillCounted(t *testing.T) {
	rc, cmds, stderr := runConfs(t, map[string]string{
		"x.conf": "q what\nu foo -:nosuch\ng ok 5\n",
	})
	if rc != 1 {
		t.Errorf("exit %d, want 1", rc)
	}
	if len(cmds) != 2 || cmds[0] != "groupadd --system --gid 5 ok" {
		t.Errorf("commands: %q", cmds)
	}
	for _, w := range []string{"u foo: useradd: group 'nosuch'", "q what: unsupported type", "1 applied, 2 failed"} {
		if !strings.Contains(stderr, w) {
			t.Errorf("stderr lacks %q:\n%s", w, stderr)
		}
	}
}
