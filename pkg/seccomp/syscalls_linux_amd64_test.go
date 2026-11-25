//go:build linux && amd64

package seccomp

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// file_getattr and file_setattr (468, 469) are in systemd's @file-system,
// but golang.org/x/sys did not name them, so the generator cut them from
// every group: ~@file-system let both through.
func TestFileSystemGroupDeniesFileAttr(t *testing.T) {
	f, err := Build([]string{"~@file-system"}, ModeAllow, nil, ActionKill, nil)
	if err != nil {
		t.Fatal(err)
	}
	has := map[int]bool{}
	for _, n := range f.SortedSyscallNumbers() {
		has[n] = true
	}
	for name, nr := range map[string]int{"file_getattr": 468, "file_setattr": 469} {
		if !has[nr] {
			t.Errorf("~@file-system does not deny %s (%d)", name, nr)
		}
	}
}

// Every number in the table, those from x/sys and those the generator
// supplies itself, must be the kernel's.
func TestSyscallNumbersMatchKernelHeader(t *testing.T) {
	hdr, err := os.ReadFile("/usr/include/asm/unistd_64.h")
	if err != nil {
		t.Skip("no kernel header to compare with")
	}
	for _, m := range regexp.MustCompile(`(?m)^#define __NR_([a-z0-9_]+) (\d+)$`).FindAllSubmatch(hdr, -1) {
		name := string(m[1])
		want, _ := strconv.Atoi(string(m[2]))
		if got, ok := SyscallNumber(name); ok && got != want {
			t.Errorf("%s = %d in the table, %d in the kernel header", name, got, want)
		}
	}
}
