package shutdown

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
	"golang.org/x/sys/unix"
)

// A sync stuck on a dead NFS server used to hold Execute for ever, short
// of the reboot syscall, after every process was gone. It now gives up
// once the dirty totals stop shrinking and reboots anyway.
func TestExecuteRebootsPastAHungSync(t *testing.T) {
	origGetpid, origKill, origSync, origReboot := getpidFunc, killFunc, syncFunc, rebootFunc
	origHook, origLogout, origLogShut, origGrace := runHookFunc, logoutAllUsersFunc, logShutdownFunc, killGracePeriod
	origInterval, origMeminfo := syncCheckInterval, meminfoPath
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		getpidFunc, killFunc, syncFunc, rebootFunc = origGetpid, origKill, origSync, origReboot
		runHookFunc, logoutAllUsersFunc, logShutdownFunc, killGracePeriod = origHook, origLogout, origLogShut, origGrace
		syncCheckInterval, meminfoPath = origInterval, origMeminfo
	})

	meminfo := filepath.Join(t.TempDir(), "meminfo")
	if err := os.WriteFile(meminfo, []byte("Dirty:  4096 kB\nWriteback:  12 kB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	meminfoPath = meminfo
	syncCheckInterval = 20 * time.Millisecond

	getpidFunc = func() int { return 1 }
	killFunc = func(int, syscall.Signal) error { return syscall.ESRCH }
	runHookFunc = func(service.ShutdownType, *logging.Logger) bool { return true }
	logoutAllUsersFunc = func() int { return 0 }
	logShutdownFunc = func() bool { return true }
	killGracePeriod = 0
	syncFunc = func() { <-release } // the server is gone

	rebooted := make(chan struct{})
	rebootFunc = func(int) error {
		close(rebooted)
		select {} // keep Execute from reaching InfiniteHold
	}
	go Execute(service.ShutdownReboot, logging.New(logging.LevelError))

	select {
	case <-rebooted:
	case <-time.After(5 * time.Second):
		t.Fatal("Execute never reached the reboot syscall: it is still waiting for sync")
	}
}

func TestPendingWritebackKB(t *testing.T) {
	orig := meminfoPath
	t.Cleanup(func() { meminfoPath = orig })
	meminfoPath = filepath.Join(t.TempDir(), "meminfo")
	os.WriteFile(meminfoPath, []byte("MemFree: 100 kB\nDirty: 40 kB\nWriteback: 2 kB\nNFS_Unstable: 1 kB\n"), 0o644)
	if got, ok := pendingWritebackKB(); !ok || got != 43 {
		t.Errorf("pendingWritebackKB = %d, %v; want 43, true", got, ok)
	}
}

// An unmount that blocks for good (a dead network server) used to hold
// unmountAll, and so the whole shutdown, for ever. Past umountTimeout
// the mount is detached and the rest are still unmounted.
func TestUnmountAllDetachesAHungMount(t *testing.T) {
	mounts := filepath.Join(t.TempDir(), "mounts")
	os.WriteFile(mounts, []byte("/dev/sda1 / ext4 rw 0 0\n"+
		"server:/export /mnt/nfs nfs4 rw 0 0\n"+
		"/dev/sda2 /home ext4 rw 0 0\n"), 0o644)

	origProc, origUmount, origMount, origTimeout := unmountProcPath, unmountFunc, mountFunc, umountTimeout
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		unmountProcPath, unmountFunc, mountFunc, umountTimeout = origProc, origUmount, origMount, origTimeout
	})
	unmountProcPath = mounts
	umountTimeout = 50 * time.Millisecond

	var mu sync.Mutex
	var calls []string
	unmountFunc = func(target string, flags int) error {
		mu.Lock()
		calls = append(calls, target+":"+flagName(flags))
		mu.Unlock()
		if target == "/mnt/nfs" && flags != unix.MNT_DETACH {
			<-release // the server is gone
		}
		return nil
	}
	mountFunc = func(string, string, string, uintptr, string) error { return nil }

	done := make(chan struct{})
	go func() {
		unmountAll(logging.New(logging.LevelError))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("unmountAll is still waiting for the hung unmount")
	}

	mu.Lock()
	defer mu.Unlock()
	want := map[string]bool{"/mnt/nfs:force": true, "/mnt/nfs:detach": true, "/home:none": true}
	for _, c := range calls {
		delete(want, c)
	}
	if len(want) != 0 {
		t.Errorf("unmount calls %v are missing %v", calls, want)
	}
}

func flagName(flags int) string {
	switch flags {
	case 0:
		return "none"
	case unix.MNT_FORCE:
		return "force"
	case unix.MNT_DETACH:
		return "detach"
	}
	return "other"
}

func TestIsNetworkFS(t *testing.T) {
	for fs, want := range map[string]bool{
		"nfs": true, "nfs4": true, "cifs": true, "smb3": true, "ceph": true,
		"fuse.sshfs": true, "fuse.glusterfs": true, "glusterfs": true,
		"ext4": false, "tmpfs": false, "fuse.gvfsd-fuse": false, "overlay": false,
	} {
		if got := isNetworkFS(fs); got != want {
			t.Errorf("isNetworkFS(%q) = %v, want %v", fs, got, want)
		}
	}
}
