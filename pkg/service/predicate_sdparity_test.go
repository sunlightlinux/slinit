package service

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// Predicates that answer as systemd's Condition*= do. Each case is
// what systemd's src/shared/condition.c and its helpers answer on the
// same machine; the fixture machine comes from withPredicateRoot.

// -------- architecture ----------------------------------------------------

func TestArchitectureSystemdNames(t *testing.T) {
	for goarch, cases := range map[string][]predCase{
		"amd64": {
			{"x86-64", false, true}, // systemd's name
			{"x86_64", false, true}, // slinit's before 4.1
			{"native", false, true},
			{"x86", false, false},
			{"amd64", false, false},
			{"arm64", false, false},
			{"x86-64", true, false},
		},
		"386":      {{"x86", false, true}, {"x86-64", false, false}, {"x86_64", false, false}},
		"arm64":    {{"arm64", false, true}, {"arm", false, false}, {"aarch64", false, false}},
		"ppc64le":  {{"ppc64-le", false, true}, {"ppc64", false, false}},
		"mipsle":   {{"mips-le", false, true}, {"mipsle", false, true}, {"mips", false, false}},
		"loong64":  {{"loongarch64", false, true}, {"loong64", false, true}},
		"riscv64":  {{"riscv64", false, true}, {"native", false, true}},
		"mips64le": {{"mips64-le", false, true}},
	} {
		t.Run(goarch, func(t *testing.T) {
			withPredicateRoot(t)
			predicateGOARCH = goarch
			runPred(t, PredArchitecture, cases)
		})
	}
}

// -------- cpu-feature -----------------------------------------------------

func TestCPUFeatureByArch(t *testing.T) {
	// "vmx flags" comes first here (the kernel prints it after
	// "flags") so that a match on the suffix would find it.
	const x86 = "processor\t: 0\nvendor_id\t: GenuineIntel\n" +
		"vmx flags\t: vnmi ept\n" +
		"flags\t\t: fpu vme pclmulqdq fma avx2 sha_ni\n" +
		"processor\t: 1\nflags\t\t: fpu\n"
	// arm64 names the hwcaps on "Features"; systemd's AT_HWCAP table
	// copies the kernel's names.
	const arm64 = "processor\t: 0\nBogoMIPS\t: 48.00\n" +
		"Features\t: fp asimd evtstrm aes pmull sha1 sha2 crc32 atomics bti\n" +
		"CPU implementer\t: 0x41\n"
	t.Run("x86-64", func(t *testing.T) {
		root := withPredicateRoot(t)
		put(t, root, "/proc/cpuinfo", x86)
		runPred(t, PredCPUFeature, []predCase{
			{"avx2", false, true},
			{"AVX2", false, true},
			{"sha_ni", false, true},
			// systemd's names for two flags the kernel spells otherwise.
			{"pclmul", false, true},
			{"fma3", false, true},
			{"pclmulqdq", false, true},
			{"avx512f", false, false},
			{"ept", false, false}, // a "vmx flags" word is no CPU flag
			{"x86-64.avx2", false, true},
			{"x86_64.avx2", false, true},
			{"arm64.avx2", false, false},
			{"avx2", true, false},
		})
	})
	t.Run("arm64", func(t *testing.T) {
		root := withPredicateRoot(t)
		predicateGOARCH = "arm64"
		put(t, root, "/proc/cpuinfo", arm64)
		runPred(t, PredCPUFeature, []predCase{
			{"aes", false, true},
			{"atomics", false, true},
			{"bti", false, true},
			{"arm64.bti", false, true},
			{"x86-64.bti", false, false},
			{"sve", false, false},
			{"fma3", false, false}, // the x86 aliases are x86 only
			{"aes", true, false},
		})
	})
	t.Run("arm64 without Features line", func(t *testing.T) {
		root := withPredicateRoot(t)
		predicateGOARCH = "arm64"
		put(t, root, "/proc/cpuinfo", x86)
		runPred(t, PredCPUFeature, []predCase{{"fpu", false, false}})
	})
	// systemd's has_cpu_with_flag is false on any other architecture,
	// whatever /proc/cpuinfo says.
	t.Run("riscv64", func(t *testing.T) {
		root := withPredicateRoot(t)
		predicateGOARCH = "riscv64"
		put(t, root, "/proc/cpuinfo", x86+arm64)
		runPred(t, PredCPUFeature, []predCase{{"fpu", false, false}, {"aes", false, false}, {"aes", true, true}})
	})
}

// -------- directory-not-empty --------------------------------------------

func TestDirectoryNotEmptySkipsHiddenAndBackup(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	touch := func(d, name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(d, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	empty := mk("empty")
	ignored := mk("ignored")
	for _, n := range []string{".hidden", "foo~", "a.rpmnew", "b.dpkg-old", "c.ucf-dist", "d.swp",
		"e.bak", "f.old", "g.new", "h.ignore", "lost+found", "aquota.user", "aquota.group"} {
		touch(ignored, n)
	}
	real := mk("real")
	touch(real, ".hidden")
	touch(real, "x.conf")
	dotdir := mk("dotdir")
	mk("dotdir/.git")
	// A name that only looks like a backup somewhere other than its
	// suffix is real.
	tricky := mk("tricky")
	touch(tricky, "new.conf")
	file := filepath.Join(dir, "file")
	touch(dir, "file")
	// A FIFO: ENOTDIR at once, not an open that waits for a writer.
	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	runPred(t, PredDirectoryNotEmpty, []predCase{
		{empty, false, false},
		{ignored, false, false},
		{real, false, true},
		{dotdir, false, false},
		{tricky, false, true},
		{file, false, false},                          // ENOTDIR
		{fifo, false, false},                          // ENOTDIR, not a hang
		{filepath.Join(dir, "missing"), false, false}, // ENOENT
		{filepath.Join(file, "sub"), false, false},    // ENOTDIR on the way
		{ignored, true, true},
		{real, true, false},
	})
	// A directory that cannot be read cannot be shown empty: systemd
	// counts it as not empty. Root reads anything, so only as a user.
	if os.Geteuid() != 0 {
		locked := mk("locked")
		if err := os.Chmod(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(locked, 0o755) })
		runPred(t, PredDirectoryNotEmpty, []predCase{{locked, false, true}, {locked, true, false}})
	}
}

func TestHiddenOrBackupFile(t *testing.T) {
	for name, want := range map[string]bool{
		".x": true, "x~": true, "lost+found": true, "aquota.user": true, "a.rpmsave": true,
		"a.rpmorig": true, "a.dpkg-new": true, "a.dpkg-tmp": true, "a.dpkg-dist": true,
		"a.dpkg-bak": true, "a.dpkg-backup": true, "a.dpkg-remove": true, "a.ucf-new": true,
		"a.ucf-old": true, "x": false, "x.conf": false, "new": false, "a.b.conf": false,
		"lost+found2": false, "a.NEW": false, "x.conf.bak": true, "x.bak.conf": false,
	} {
		if got := hiddenOrBackupFile(name); got != want {
			t.Errorf("hiddenOrBackupFile(%q) = %v, want %v", name, got, want)
		}
	}
}

// -------- security=audit ----------------------------------------------------

func TestAuditUsable(t *testing.T) {
	for _, tc := range []struct {
		sock, req error
		want      bool
	}{
		{nil, nil, true},
		{unix.EPROTONOSUPPORT, nil, false}, // kernel without CONFIG_AUDIT
		{unix.EAFNOSUPPORT, nil, false},
		{unix.EPERM, nil, false},
		{unix.EACCES, nil, false},
		{unix.EMFILE, nil, true}, // says nothing about audit
		{errors.New("odd"), nil, true},
		{nil, unix.ECONNREFUSED, false}, // not the initial user namespace
		{nil, unix.EPERM, true},         // GET_FEATURE wants CAP_AUDIT_CONTROL; audit is there
		{nil, unix.EIO, true},
		{nil, unix.EAGAIN, true},
	} {
		if got := auditUsable(tc.sock, tc.req); got != tc.want {
			t.Errorf("auditUsable(%v, %v) = %v, want %v", tc.sock, tc.req, got, tc.want)
		}
	}
}

// -------- path-is-mount-point ----------------------------------------------

// TestPathIsMountPointStatx: the kernel's mount-root attribute decides,
// then the mount id, then the device; the device alone is wrong both
// ways (a bind mount within one filesystem, a btrfs subvolume).
func TestPathIsMountPointStatx(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	orig := mountStatx
	t.Cleanup(func() { mountStatx = orig })
	type answer struct {
		mask, attrMask, attr uint64
		mnt                  uint64
		ino                  uint64
		err                  error
	}
	fake := func(byPath map[string]answer) {
		mountStatx = func(dirfd int, path string, flags int, mask int, stx *unix.Statx_t) error {
			a, ok := byPath[path]
			if !ok {
				return orig(dirfd, path, flags, mask, stx)
			}
			if a.err != nil {
				return a.err
			}
			*stx = unix.Statx_t{Mask: uint32(a.mask), Attributes_mask: a.attrMask, Attributes: a.attr, Mnt_id: a.mnt, Ino: a.ino}
			return nil
		}
	}
	const root = unix.STATX_ATTR_MOUNT_ROOT
	// A bind mount within one filesystem: same device as the parent,
	// but the root of a mount.
	fake(map[string]answer{sub: {mask: unix.STATX_MNT_ID, attrMask: root, attr: root, mnt: 7, ino: 99}})
	if ok, why := pathIsMountPoint(sub); !ok {
		t.Errorf("mount-root attribute set: %v %q, want true", ok, why)
	}
	// A btrfs subvolume: its own device, not a mount.
	fake(map[string]answer{sub: {mask: unix.STATX_MNT_ID, attrMask: root, attr: 0, mnt: 7, ino: 99}})
	if ok, _ := pathIsMountPoint(sub); ok {
		t.Errorf("mount-root attribute clear: true, want false")
	}
	// No mount-root attribute (before 5.8): the mount ids.
	fake(map[string]answer{
		sub: {mask: unix.STATX_MNT_ID, mnt: 8},
		dir: {mask: unix.STATX_MNT_ID, mnt: 7},
	})
	if ok, why := pathIsMountPoint(sub); !ok {
		t.Errorf("mount id differs from the parent's: %v %q, want true", ok, why)
	}
	fake(map[string]answer{
		sub: {mask: unix.STATX_MNT_ID, mnt: 7},
		dir: {mask: unix.STATX_MNT_ID, mnt: 7},
	})
	if ok, _ := pathIsMountPoint(sub); ok {
		t.Errorf("same mount id as the parent: true, want false")
	}
	// No statx at all: the device comparison (a tmpdir is not a mount
	// unless the host made it one).
	fake(map[string]answer{sub: {err: unix.ENOSYS}})
	if ok, _ := pathIsMountPoint(sub); ok {
		t.Errorf("ENOSYS fallback on a plain directory: true, want false")
	}
	fake(map[string]answer{sub: {err: unix.EACCES}})
	if ok, why := pathIsMountPoint(sub); ok || !strings.Contains(why, "permission") {
		t.Errorf("statx error: %v %q, want false naming it", ok, why)
	}
}

// TestPathIsMountPointBindMount makes a bind mount of a directory onto
// another in the same filesystem, in a user and mount namespace of a
// child process, and asks the real kernel. The device comparison
// slinit used said no.
func TestPathIsMountPointBindMount(t *testing.T) {
	if dir := os.Getenv("SLINIT_BINDTEST_DIR"); dir != "" {
		bindMountChild(dir)
		return
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPathIsMountPointBindMount$", "-test.count=1")
	cmd.Env = append(os.Environ(), "SLINIT_BINDTEST_DIR="+dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
	out, err := cmd.CombinedOutput()
	s := string(out)
	switch {
	case strings.Contains(s, "BINDTEST-SKIP"):
		t.Skipf("cannot bind mount here: %s", s)
	case err != nil && !strings.Contains(s, "BINDTEST-"):
		// The namespaces themselves were refused (AppArmor, no
		// unprivileged user namespaces).
		t.Skipf("no user+mount namespace: %v: %s", err, s)
	case !strings.Contains(s, "BINDTEST-OK"):
		t.Fatalf("child: %v\n%s", err, s)
	}
}

func bindMountChild(dir string) {
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	os.Mkdir(src, 0o755)
	os.Mkdir(dst, 0o755)
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		println("BINDTEST-SKIP private:", err.Error())
		return
	}
	if err := unix.Mount(src, dst, "", unix.MS_BIND, ""); err != nil {
		println("BINDTEST-SKIP bind:", err.Error())
		return
	}
	var a, b unix.Stat_t
	unix.Stat(dst, &a)
	unix.Stat(dir, &b)
	if a.Dev != b.Dev {
		println("BINDTEST-SKIP: the bind is not within one device")
		return
	}
	got, why := pathIsMountPoint(dst)
	parent, _ := pathIsMountPoint(dir)
	srcOK, _ := pathIsMountPoint(src)
	if got && !srcOK && !parent {
		println("BINDTEST-OK")
		return
	}
	println("BINDTEST-FAIL dst:", got, why, "src:", srcOK, "parent:", parent)
}
