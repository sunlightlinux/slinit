package features

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Two ways to end up with two Go objects owning one file descriptor, both
// of which close it. The second close lands on whatever number the kernel
// handed out in the meantime — a file belonging to someone else entirely.
//
// This is not hypothetical. A single discarded
// `os.NewFile(w.Fd(), "pipe-write")` in a logbuffer test cost three weeks
// of an intermittent failure that looked like flakiness: an unrelated
// test died in `t.TempDir()` cleanup with "bad file descriptor", never in
// an assertion, and never when its own package was run on its own.
// Attributed with
// `strace -f -k -e trace=close -e status=failed`, which showed
// `close(6) = -1 EBADF` under `runtime.runFinalizers` ->
// `os.(*file).close`: the orphaned second owner being collected.
//
// So the pattern is banned by a test rather than by a comment somebody
// has to remember. Same approach as the since-marker test next door.
//
//   - os.NewFile(x.Fd(), …) — wraps a descriptor another *os.File owns.
//     If the fd really must be handed to something else, dup it
//     (syscall.Dup) and wrap the dup, or use net.FileConn/FileListener,
//     which dup internally.
//   - syscall.Close(int(x.Fd())) / unix.Close(…) — closes a descriptor
//     behind its *os.File's back; the File's own Close, or its finalizer,
//     then closes a number it no longer owns. Call x.Close() instead.
var fdOwnershipPatterns = []struct {
	re   *regexp.Regexp
	what string
	fix  string
}{
	{
		regexp.MustCompile(`os\.NewFile\(\s*[A-Za-z_][\w.]*\.Fd\(\)`),
		"os.NewFile over a descriptor an *os.File already owns",
		"dup it first (syscall.Dup), or use net.FileConn/net.FileListener, which dup internally",
	},
	{
		regexp.MustCompile(`(syscall|unix)\.Close\(\s*int\(\s*[A-Za-z_][\w.]*\.Fd\(\)`),
		"raw close of a descriptor an *os.File owns",
		"call the file's own Close method",
	},
}

func TestNoDoubleOwnedFileDescriptors(t *testing.T) {
	root := "../.."

	var findings []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "_output", "_build", "vendor", "demo":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// This file names the patterns it bans.
		if filepath.Base(path) == "fdownership_test.go" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(b), "\n") {
			code := line
			if i := strings.Index(code, "//"); i >= 0 {
				code = code[:i]
			}
			for _, p := range fdOwnershipPatterns {
				if p.re.MatchString(code) {
					findings = append(findings,
						strings.TrimPrefix(path, root+"/")+": "+p.what+
							" — "+p.fix+"\n    "+strings.TrimSpace(line))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(findings) > 0 {
		t.Errorf("%d descriptor-ownership violation(s). Each one means two "+
			"objects can close the same fd, and the loser closes a file "+
			"that by then belongs to something else:\n\n%s",
			len(findings), strings.Join(findings, "\n"))
	}
}
