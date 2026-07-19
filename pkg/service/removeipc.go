package service

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// removeIPCForUID cleans up SysV IPC + POSIX shm objects owned by the
// given UID. Mirrors systemd's RemoveIPC= semantics: called after a
// service stops so a subsequent restart doesn't inherit stale IPC
// state from the previous instance. UID 0 (root) is skipped — cleaning
// root-owned IPC would hit shared system state (dbus, X server, etc.).
//
// Best-effort by design: a file races-removed between Readdir and
// Remove is ignored (ENOENT is fine); errors on individual objects
// don't abort the sweep. Leaving some IPC behind is preferable to
// skipping the whole cleanup on the first failure.
func removeIPCForUID(uid uint32) {
	if uid == 0 {
		return
	}
	sweepPosixShm(uid)
	sweepSysVIPC(uid)
}

func sweepPosixShm(uid uint32) {
	entries, err := os.ReadDir("/dev/shm")
	if err != nil {
		return
	}
	for _, e := range entries {
		full := filepath.Join("/dev/shm", e.Name())
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != uid {
			continue
		}
		_ = os.Remove(full)
	}
}

// sweepSysVIPC parses /proc/sysvipc/{shm,msg,sem}. Each file's first
// row is a header; subsequent rows have whitespace-separated columns
// with a fixed layout — column 1 (0-indexed) is the object id, column
// 4 is the UID. Anything whose UID matches is destroyed via the
// matching IPC_RMID call.
func sweepSysVIPC(uid uint32) {
	sweepIPC("/proc/sysvipc/shm", uid, rmidShm)
	sweepIPC("/proc/sysvipc/msg", uid, rmidMsg)
	sweepIPC("/proc/sysvipc/sem", uid, rmidSem)
}

func sweepIPC(path string, uid uint32, rmid func(id int)) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) <= 1 {
		return
	}
	for _, ln := range lines[1:] {
		fields := strings.Fields(ln)
		if len(fields) < 5 {
			continue
		}
		id, err1 := strconv.Atoi(fields[1])
		u, err2 := strconv.ParseUint(fields[4], 10, 32)
		if err1 != nil || err2 != nil {
			continue
		}
		if uint32(u) == uid {
			rmid(id)
		}
	}
}

// rmidShm/rmidMsg/rmidSem invoke shmctl(IPC_RMID), msgctl(IPC_RMID),
// semctl(IPC_RMID). unix has SYS_SHMCTL/SYS_MSGCTL/SYS_SEMCTL constants;
// call them directly rather than depending on a per-arch typed wrapper.
func rmidShm(id int) {
	unix.Syscall(unix.SYS_SHMCTL, uintptr(id), unix.IPC_RMID, 0)
}

func rmidMsg(id int) {
	unix.Syscall(unix.SYS_MSGCTL, uintptr(id), unix.IPC_RMID, 0)
}

func rmidSem(id int) {
	// semctl signature is semctl(semid, semnum, cmd, arg); semnum
	// ignored for IPC_RMID.
	unix.Syscall(unix.SYS_SEMCTL, uintptr(id), 0, unix.IPC_RMID)
}
