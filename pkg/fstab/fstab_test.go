package fstab

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseHappyPath(t *testing.T) {
	in := `# comment line
/dev/sda1  /            ext4  defaults,noatime  0 1
/dev/sda2  none         swap  sw                0 0
UUID=abc   /home        ext4  rw,relatime       0 2
tmpfs      /tmp         tmpfs mode=1777         0 0
`
	entries, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("len=%d", len(entries))
	}
	if entries[0].Spec != "/dev/sda1" || entries[0].File != "/" ||
		entries[0].VFSType != "ext4" || entries[0].MntOps != "defaults,noatime" ||
		entries[0].Freq != 0 || entries[0].PassNo != 1 {
		t.Errorf("entry 0: %+v", entries[0])
	}
	if entries[2].Spec != "UUID=abc" || entries[2].PassNo != 2 {
		t.Errorf("entry 2: %+v", entries[2])
	}
}

func TestParseSkipsBlankAndComment(t *testing.T) {
	in := `

# hash-only line
    # indented comment
/dev/sda1 /boot ext4 defaults 0 2

`
	entries, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("len=%d", len(entries))
	}
}

func TestParseFourAndFiveFields(t *testing.T) {
	in := `/dev/sda1 / ext4 defaults
/dev/sda2 /home ext4 defaults 1
`
	entries, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].Freq != 0 || entries[0].PassNo != 0 {
		t.Errorf("entry 0 defaults: %+v", entries[0])
	}
	if entries[1].Freq != 1 || entries[1].PassNo != 0 {
		t.Errorf("entry 1: %+v", entries[1])
	}
}

func TestParseRejectsBadArity(t *testing.T) {
	if _, err := Parse(strings.NewReader("/dev/sda1 /\n")); err == nil {
		t.Errorf("expected error for 2-field line")
	}
	if _, err := Parse(strings.NewReader("a b c d e f g\n")); err == nil {
		t.Errorf("expected error for 7-field line")
	}
}

func TestParseUnescape(t *testing.T) {
	// Space in mountpoint: /mnt/my\040drive
	in := "/dev/sdb1 /mnt/my\\040drive ext4 defaults 0 0\n"
	entries, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].File != "/mnt/my drive" {
		t.Errorf("unescape failed: %q", entries[0].File)
	}
}

func TestFindByFile(t *testing.T) {
	entries := []Entry{
		{Spec: "/dev/a", File: "/"},
		{Spec: "/dev/b", File: "/home"},
	}
	if got := FindByFile(entries, "/home"); got == nil || got.Spec != "/dev/b" {
		t.Errorf("FindByFile(/home) = %+v", got)
	}
	if got := FindByFile(entries, "/nope"); got != nil {
		t.Errorf("FindByFile(nope) = %+v, want nil", got)
	}
}

func TestOptionsSplitsAndTrims(t *testing.T) {
	e := Entry{MntOps: "rw, noatime,  nosuid"}
	got := e.Options()
	want := []string{"rw", "noatime", "nosuid"}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: %v vs %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("Options[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if e2 := (Entry{}); e2.Options() != nil {
		t.Errorf("empty Options should be nil")
	}
}

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fstab")
	if err := os.WriteFile(path, []byte("/dev/sda1 / ext4 defaults 0 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].File != "/" {
		t.Errorf("read: %+v", entries)
	}
}
