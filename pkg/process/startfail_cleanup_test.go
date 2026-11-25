package process

import (
	"path/filepath"
	"strings"
	"testing"
)

// A start that failed after the lock-file was taken (a missing
// standard-input-file, an open-file that cannot be opened, …) returned
// without closing the lock's descriptor, so its flock stayed held and
// every later start failed "lock-file already locked" until slinit
// restarted. Only a cmd.Start failure cleaned up. Before the fix the
// second start below failed with exactly that error.
func TestFailedStartReleasesLockFile(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		bad  func(*ExecParams)
	}{
		{"standard-input-file", func(p *ExecParams) { p.StdinFile = filepath.Join(dir, "missing") }},
		{"open-file", func(p *ExecParams) {
			p.OpenFiles = []OpenFileEntry{{Path: filepath.Join(dir, "no", "such", "file"), Options: "read-only"}}
		}},
	} {
		lock := filepath.Join(dir, tc.name+".lock")
		p := ExecParams{ServiceName: "svc", Command: []string{"/bin/true"}, LockFile: lock}
		tc.bad(&p)
		if _, _, err := StartProcess(p); err == nil {
			t.Fatalf("%s: misconfigured start succeeded", tc.name)
		}

		p = ExecParams{ServiceName: "svc", Command: []string{"/bin/true"}, LockFile: lock}
		_, ch, err := StartProcess(p)
		if err != nil {
			if strings.Contains(err.Error(), "already locked") {
				t.Errorf("after a failed start (%s) the next start found the lock still held: %v", tc.name, err)
			} else {
				t.Errorf("%s: second start: %v", tc.name, err)
			}
			continue
		}
		<-ch
	}
}
