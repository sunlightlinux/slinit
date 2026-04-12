package shutdown

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/logging"
)

func TestUnescapeMount(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/mnt/plain", "/mnt/plain"},
		{`/mnt/with\040space`, "/mnt/with space"},
		{`/mnt/tab\011here`, "/mnt/tab\there"},
		{`/mnt/double\\slash`, `/mnt/double\\slash`}, // not a valid octal escape → leave as-is
		{`/mnt/short\04`, `/mnt/short\04`},           // incomplete escape at end
	}
	for _, tc := range cases {
		if got := unescapeMount(tc.in); got != tc.want {
			t.Errorf("unescapeMount(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseMounts(t *testing.T) {
	sample := `rootfs / rootfs rw 0 0
/dev/sda1 / ext4 rw,relatime 0 0
proc /proc proc rw 0 0
tmpfs /run/user/1000\040data tmpfs rw 0 0
`
	entries, err := parseMounts(strings.NewReader(sample))
	if err != nil {
		t.Fatalf("parseMounts: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(entries))
	}

	want := []mountEntry{
		{source: "rootfs", target: "/", fstype: "rootfs", opts: "rw"},
		{source: "/dev/sda1", target: "/", fstype: "ext4", opts: "rw,relatime"},
		{source: "proc", target: "/proc", fstype: "proc", opts: "rw"},
		{source: "tmpfs", target: "/run/user/1000 data", fstype: "tmpfs", opts: "rw"},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("parseMounts mismatch:\n got: %+v\nwant: %+v", entries, want)
	}
}

func TestReadMounts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mounts")
	if err := os.WriteFile(path, []byte("/dev/sda1 / ext4 rw 0 0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	entries, err := readMounts(path)
	if err != nil {
		t.Fatalf("readMounts: %v", err)
	}
	if len(entries) != 1 || entries[0].target != "/" {
		t.Errorf("unexpected entries: %+v", entries)
	}
}

func TestSortMountsReverse(t *testing.T) {
	entries := []mountEntry{
		{target: "/"},
		{target: "/home"},
		{target: "/home/user"},
		{target: "/mnt"},
		{target: "/home/user/data"},
	}
	sortMountsReverse(entries)

	// Deepest first.
	if entries[0].target != "/home/user/data" {
		t.Errorf("first = %q, want /home/user/data", entries[0].target)
	}
	// Root last.
	if entries[len(entries)-1].target != "/" {
		t.Errorf("last = %q, want /", entries[len(entries)-1].target)
	}

	// Sanity: every element that comes before another must be at least as deep.
	for i := 0; i < len(entries)-1; i++ {
		if len(entries[i].target) < len(entries[i+1].target) {
			t.Errorf("sort order violation: %q before %q",
				entries[i].target, entries[i+1].target)
		}
	}
}

func TestShouldSkipUnmount(t *testing.T) {
	if !shouldSkipUnmount(mountEntry{target: "/"}) {
		t.Error("/ should be skipped")
	}
	if shouldSkipUnmount(mountEntry{target: "/proc"}) {
		t.Error("/proc should not be skipped")
	}
	if shouldSkipUnmount(mountEntry{target: "/home/user"}) {
		t.Error("/home/user should not be skipped")
	}
}

func TestParseSwaps(t *testing.T) {
	sample := `Filename                                Type            Size            Used            Priority
/dev/sda2                               partition       8388604         0               -2
/swapfile\040backup                     file            524288          0               -3
`
	devs, err := parseSwaps(strings.NewReader(sample))
	if err != nil {
		t.Fatalf("parseSwaps: %v", err)
	}
	want := []string{"/dev/sda2", "/swapfile backup"}
	if !reflect.DeepEqual(devs, want) {
		t.Errorf("parseSwaps = %v, want %v", devs, want)
	}
}

func TestParseSwapsEmpty(t *testing.T) {
	// Header only → no active swap.
	sample := "Filename  Type  Size  Used  Priority\n"
	devs, err := parseSwaps(strings.NewReader(sample))
	if err != nil {
		t.Fatalf("parseSwaps: %v", err)
	}
	if len(devs) != 0 {
		t.Errorf("expected no devices, got %v", devs)
	}
}

func TestUnmountAllCallsSyscalls(t *testing.T) {
	// Build a fake /proc/mounts with several mount points.
	dir := t.TempDir()
	mountsPath := filepath.Join(dir, "mounts")
	content := `/dev/sda1 / ext4 rw 0 0
tmpfs /run tmpfs rw 0 0
/dev/sda2 /home ext4 rw 0 0
/dev/sda3 /home/user/data ext4 rw 0 0
`
	if err := os.WriteFile(mountsPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	origProc := unmountProcPath
	origUmount := unmountFunc
	origMount := mountFunc
	t.Cleanup(func() {
		unmountProcPath = origProc
		unmountFunc = origUmount
		mountFunc = origMount
	})
	unmountProcPath = mountsPath

	var unmountCalls []string
	unmountFunc = func(target string, flags int) error {
		unmountCalls = append(unmountCalls, target)
		return nil
	}
	var mountCalls []string
	mountFunc = func(source, target, fstype string, flags uintptr, data string) error {
		mountCalls = append(mountCalls, target)
		return nil
	}

	unmountAll(logging.New(logging.LevelDebug))

	// / must NOT appear in unmount calls.
	for _, t := range unmountCalls {
		if t == "/" {
			// fail
		}
	}
	for i, target := range unmountCalls {
		if target == "/" {
			t.Errorf("unmount call %d attempted /", i)
		}
	}

	// Deepest target (/home/user/data) must come before /home.
	idxData, idxHome := -1, -1
	for i, target := range unmountCalls {
		if target == "/home/user/data" {
			idxData = i
		}
		if target == "/home" {
			idxHome = i
		}
	}
	if idxData < 0 || idxHome < 0 {
		t.Fatalf("missing mounts: calls=%v", unmountCalls)
	}
	if idxData > idxHome {
		t.Errorf("/home/user/data must unmount before /home (calls=%v)", unmountCalls)
	}

	// Final mount call should be / remount ro.
	if len(mountCalls) == 0 || mountCalls[len(mountCalls)-1] != "/" {
		t.Errorf("expected final mount call to be /, got %v", mountCalls)
	}
}

func TestUnmountOneFallsBackToDetach(t *testing.T) {
	origUmount := unmountFunc
	origMount := mountFunc
	t.Cleanup(func() {
		unmountFunc = origUmount
		mountFunc = origMount
	})

	calls := 0
	unmountFunc = func(target string, flags int) error {
		calls++
		if calls == 1 {
			return syscall.EBUSY
		}
		return nil // second call (MNT_DETACH) succeeds
	}
	mountFunc = func(source, target, fstype string, flags uintptr, data string) error {
		t.Errorf("remount should not be called when detach succeeded")
		return nil
	}

	unmountOne(mountEntry{target: "/home"}, logging.New(logging.LevelDebug))
	if calls != 2 {
		t.Errorf("expected 2 unmount attempts, got %d", calls)
	}
}

func TestUnmountOneRemountsReadOnlyOnTotalFailure(t *testing.T) {
	origUmount := unmountFunc
	origMount := mountFunc
	t.Cleanup(func() {
		unmountFunc = origUmount
		mountFunc = origMount
	})

	unmountFunc = func(target string, flags int) error { return syscall.EBUSY }
	mounted := ""
	mountFunc = func(source, target, fstype string, flags uintptr, data string) error {
		mounted = target
		return nil
	}

	unmountOne(mountEntry{target: "/home"}, logging.New(logging.LevelDebug))
	if mounted != "/home" {
		t.Errorf("expected /home to be remounted ro, got %q", mounted)
	}
}

func TestSwapOffCallsEachDevice(t *testing.T) {
	dir := t.TempDir()
	swapsPath := filepath.Join(dir, "swaps")
	content := `Filename  Type  Size  Used  Priority
/dev/sda2  partition  8388604  0  -2
/swapfile  file  524288  0  -3
`
	if err := os.WriteFile(swapsPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	origPath := swapsProcPath
	origFn := swapoffFunc
	t.Cleanup(func() {
		swapsProcPath = origPath
		swapoffFunc = origFn
	})
	swapsProcPath = swapsPath

	var called []string
	swapoffFunc = func(path string) error {
		called = append(called, path)
		return nil
	}

	swapOff(logging.New(logging.LevelDebug))

	want := []string{"/dev/sda2", "/swapfile"}
	if !reflect.DeepEqual(called, want) {
		t.Errorf("swapoff calls = %v, want %v", called, want)
	}
}
