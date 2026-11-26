package service

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// foreignRootPID returns a live process owned by root that is neither
// init nor us: the kind of process an unprivileged service must not be
// able to make slinit signal by naming it in its pid file.
func foreignRootPID(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Skip("no /proc")
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == 1 || pid == os.Getpid() {
			continue
		}
		f, err := os.Open("/proc/" + e.Name() + "/status")
		if err != nil {
			continue
		}
		root := false
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if u := strings.Fields(sc.Text()); len(u) >= 4 && u[0] == "Uid:" {
				root = u[1] == "0" && u[3] == "0"
				break
			}
		}
		f.Close()
		if root {
			return pid
		}
	}
	t.Skip("no root-owned process other than init visible")
	return 0
}

// pretendManagerIsRoot makes the service set judge pid files as slinit
// running as root does, while the test (and so every file it writes)
// runs unprivileged: the file then stands for one a run-as service wrote.
func pretendManagerIsRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("needs an unprivileged test user to write a non-root pid file")
	}
	old := pidFileManagerUID
	pidFileManagerUID = func() int { return 0 }
	t.Cleanup(func() { pidFileManagerUID = old })
}

func waitSettled(svc Service) ServiceState {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && svc.State() == StateStarting {
		time.Sleep(20 * time.Millisecond)
	}
	return svc.State()
}

// A pid file owned by someone other than root may only name a process
// its owner could signal itself (or one in the service's own cgroup).
// Otherwise a run-as service writes, say, sshd's pid into its pid file
// and slinit adopts it as the daemon, then SIGTERMs/SIGKILLs it on stop.
// systemd refuses the same file (service_load_pid_file).
func TestBGProcessRefusesForeignPIDFromNonRootPIDFile(t *testing.T) {
	pretendManagerIsRoot(t)
	victim := foreignRootPID(t)
	set, _ := newTestSet()

	pidFile := filepath.Join(t.TempDir(), "evil.pid")
	svc := NewBGProcessService(set, "evil-svc")
	svc.SetCommand([]string{"/bin/sh", "-c", "echo " + strconv.Itoa(victim) + " > " + pidFile})
	svc.SetPIDFile(pidFile)
	set.AddService(svc)
	set.StartService(svc)

	st := waitSettled(svc)
	if st == StateStarted || svc.PID() == victim {
		t.Fatalf("adopted foreign root process %d from a non-root pid file: state %v, PID %d",
			victim, st, svc.PID())
	}
}

// The ordinary run-as case keeps working: the daemon runs as the user who
// wrote the file, so naming it gives slinit nothing that user lacked.
func TestBGProcessAcceptsOwnUIDPIDFromNonRootPIDFile(t *testing.T) {
	pretendManagerIsRoot(t)
	set, _ := newTestSet()

	pidFile := filepath.Join(t.TempDir(), "ok.pid")
	pid := startTestDaemon(t)
	svc := NewBGProcessService(set, "ok-svc")
	svc.SetCommand([]string{"/bin/sh", "-c", "echo " + strconv.Itoa(pid) + " > " + pidFile})
	svc.SetPIDFile(pidFile)
	set.AddService(svc)
	set.StartService(svc)

	if st := waitSettled(svc); st != StateStarted || svc.PID() != pid {
		t.Fatalf("state %v PID %d, want STARTED with %d", st, svc.PID(), pid)
	}
}

// The systemd half of the rule: a pid in the service's own cgroup is
// accepted whoever owns the file, and the same pid is refused once the
// cgroup is not the service's.
func TestPIDFileOwnerMayNameCgroupMember(t *testing.T) {
	pretendManagerIsRoot(t)
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Skip("no /proc/self/cgroup")
	}
	var own string
	for _, line := range strings.Split(string(data), "\n") {
		if rel, ok := strings.CutPrefix(line, "0::"); ok {
			own = filepath.Clean("/sys/fs/cgroup" + rel)
		}
	}
	if own == "" {
		t.Skip("not on cgroup v2")
	}
	stranger := uint32(os.Getuid() + 4242) // neither root nor the test's uid
	if err := pidFileWritersMayName([]uint32{stranger}, os.Getpid(), own); err != nil {
		t.Errorf("pid in the service's cgroup refused: %v", err)
	}
	if err := pidFileWritersMayName([]uint32{stranger}, os.Getpid(), own+"-other"); err == nil {
		t.Error("pid outside the service's cgroup (a sibling with a common prefix) accepted")
	}
	if err := pidFileWritersMayName([]uint32{stranger}, os.Getpid(), ""); err == nil {
		t.Error("pid of another uid accepted from a non-root file with no cgroup")
	}
}

// rootPIDFile returns a pid file owned by root, in a root-owned
// directory, that names a live process other than init: what a run-as
// service could steer slinit to read through a symlink of its own.
func rootPIDFile(t *testing.T) (dir, name string, pid int) {
	t.Helper()
	for _, d := range []string{"/run", "/var/run"} {
		matches, _ := filepath.Glob(filepath.Join(d, "*.pid"))
		for _, m := range matches {
			fi, err := os.Lstat(m)
			if err != nil || !fi.Mode().IsRegular() {
				continue
			}
			if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Uid != 0 {
				continue
			}
			p, res, err := process.ReadPIDFile(m)
			if err != nil || res != process.PIDResultOK {
				continue
			}
			return d, filepath.Base(m), p
		}
	}
	t.Skip("no root-owned pid file naming a live process in /run")
	return "", "", 0
}

// The file is root's, but the path to it is not: it runs through a
// directory the service's user owns, where that user put a symlink to
// /run. systemd's CHASE_SAFE flags the same chain and then refuses a pid
// outside the service's cgroup, whoever owns the file.
func TestBGProcessRefusesRootPIDFileReachedThroughUserSymlink(t *testing.T) {
	pretendManagerIsRoot(t)
	target, name, victim := rootPIDFile(t)
	set, _ := newTestSet()

	userDir := t.TempDir() // owned by the test user, standing for run-as
	if err := os.Symlink(target, filepath.Join(userDir, "run")); err != nil {
		t.Fatal(err)
	}
	svc := NewBGProcessService(set, "symlink-svc")
	svc.SetCommand([]string{"/bin/sh", "-c", "exit 0"})
	svc.SetPIDFile(filepath.Join(userDir, "run", name))
	set.AddService(svc)
	set.StartService(svc)

	st := waitSettled(svc)
	if st == StateStarted || svc.PID() == victim {
		t.Fatalf("adopted %d from %s/%s through a user-owned symlink: state %v, PID %d",
			victim, target, name, st, svc.PID())
	}
}

// A root daemon that keeps its pid file in a directory belonging to its
// service user (/run/nginx owned by nginx, the Alpine layout) was refused
// once directory owners counted as writers. Reached without a symlink,
// the file read is the one root wrote; only through a symlink (or a hard
// link) can the directory's owner make slinit read another.
func TestPIDFileInUserDirectoryWithoutSymlink(t *testing.T) {
	pretendManagerIsRoot(t)
	root := foreignRootPID(t)
	dir := t.TempDir() // owned by the test user: "the service user"
	path := filepath.Join(dir, "nginx.pid")
	rootFile := process.PIDFileInfo{Owner: 0, Nlink: 1, NoSymlinks: true}

	w, err := pidFileWriters(path, rootFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := pidFileWritersMayName(w, root, ""); err != nil {
		t.Errorf("root-owned pid file in a user directory, no symlink: %v", err)
	}

	for name, info := range map[string]process.PIDFileInfo{
		"through a symlink": {Owner: 0, Nlink: 1},
		"hard-linked":       {Owner: 0, Nlink: 2, NoSymlinks: true},
	} {
		w, err := pidFileWriters(path, info)
		if err != nil {
			t.Fatal(err)
		}
		if pidFileWritersMayName(w, root, "") == nil {
			t.Errorf("%s: root pid accepted from a file the directory's owner could have pointed at", name)
		}
	}
}

// ReadPIDFileInfo says whether the path was resolved without a symlink.
func TestReadPIDFileInfoSeesSymlinks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "x.pid"), []byte(strconv.Itoa(os.Getppid())+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	_, _, direct, err := process.ReadPIDFileInfo(filepath.Join(real, "x.pid"))
	if err != nil || !direct.NoSymlinks || direct.Nlink != 1 {
		t.Errorf("direct path: %+v, %v; want NoSymlinks, one link", direct, err)
	}
	_, _, via, err := process.ReadPIDFileInfo(filepath.Join(dir, "link", "x.pid"))
	if err != nil || via.NoSymlinks {
		t.Errorf("path through a symlink: %+v, %v; want NoSymlinks false", via, err)
	}
}
