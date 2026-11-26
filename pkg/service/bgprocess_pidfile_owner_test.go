package service

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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
