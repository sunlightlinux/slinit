package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The --verbose scratch file must not live in a world-writable
// directory under a predictable name.
//
// It used to be /tmp/slinit-supervise-daemon.err, opened
// O_CREATE|O_APPEND by a process running as root. Any local user could
// create that name first as a symlink and have root append the
// supervisor's stderr to whatever it pointed at — content injection
// into a root-owned file, which in a sudoers.d fragment or a shell rc
// is worse than it sounds. A plain file planted there instead leaks
// the supervisor's stderr to its owner. /tmp's sticky bit does not
// help: it stops you removing someone else's entry, not creating one
// that does not exist yet.
//
// This reads the source rather than calling the code, because the
// unsafe thing is the path constant and the open flags, and both are
// chosen at the call site. A test that opened the file would pass just
// as well with /tmp back in place.
func TestVerboseScratchFileIsNotInAWorldWritableDir(t *testing.T) {
	src, err := os.ReadFile("start.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)

	for _, bad := range []string{`"/tmp/`, `"/var/tmp/`, `"/dev/shm/`} {
		if strings.Contains(text, bad) {
			t.Errorf("start.go names a fixed path under %s; root must not open a predictable name in a world-writable directory",
				strings.Trim(bad, `"`))
		}
	}

	if !strings.Contains(text, `"/run/slinit/supervise-daemon.err"`) {
		t.Error("the verbose scratch file is no longer /run/slinit/supervise-daemon.err — if it moved, move this test with it")
	}
	if !strings.Contains(text, "syscall.O_NOFOLLOW") {
		t.Error("the verbose scratch file is opened without O_NOFOLLOW")
	}
}

// Nothing in this binary's production code should name a fixed path in
// a world-writable directory, not just start.go. Cheap to state once
// here so the next scratch file does not reintroduce it.
func TestNoProductionFileNamesAWorldWritablePath(t *testing.T) {
	bad := regexp.MustCompile(`"(/tmp|/var/tmp|/dev/shm)/`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var checked int
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		checked++
		for _, m := range bad.FindAllString(string(src), -1) {
			t.Errorf("%s names a fixed path %s…", f, strings.Trim(m, `"`))
		}
	}
	if checked == 0 {
		t.Fatal("no production files were read; the glob or the skip is wrong")
	}
}
