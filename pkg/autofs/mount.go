package autofs

import (
	"fmt"
	"os"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// AutofsMount manages a single autofs mount point and its kernel communication.
type AutofsMount struct {
	mountpoint string
	pipeRD     int // read end of the pipe (daemon reads kernel packets)
	pipeWR     int // write end (passed to mount, closed after setup)
	ioctlFD    int // fd opened on the mount point for ioctl
	timeout    time.Duration
	unit       *MountUnit

	mounted map[string]time.Time // sub-mount name → mount time
	mu      sync.Mutex
}

// Setup creates the autofs mount point.
// 1. Creates pipe for kernel ↔ daemon communication
// 2. Mounts autofs on the target directory
// 3. Opens mount point for ioctl control
// 4. Sets the idle timeout
func Setup(unit *MountUnit) (*AutofsMount, error) {
	// Ensure mount point directory exists
	if err := os.MkdirAll(unit.Where, unit.DirMode); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", unit.Where, err)
	}

	// Create pipe for kernel communication
	var pipeFDs [2]int
	if err := unix.Pipe2(pipeFDs[:], unix.O_CLOEXEC); err != nil {
		return nil, fmt.Errorf("pipe2: %w", err)
	}

	// Build mount options string
	opts := fmt.Sprintf("fd=%d,pgrp=%d,minproto=%d,maxproto=%d",
		pipeFDs[1], os.Getpid(), MinProtoVersion, MaxProtoVersion)

	// Mount autofs filesystem
	if err := unix.Mount("slinit-mount", unit.Where, "autofs", 0, opts); err != nil {
		unix.Close(pipeFDs[0])
		unix.Close(pipeFDs[1])
		return nil, fmt.Errorf("mount autofs on %s: %w", unit.Where, err)
	}

	// Open mount point to get an fd for ioctl
	ioctlFD, err := unix.Open(unit.Where, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		unix.Unmount(unit.Where, 0)
		unix.Close(pipeFDs[0])
		unix.Close(pipeFDs[1])
		return nil, fmt.Errorf("open %s for ioctl: %w", unit.Where, err)
	}

	am := &AutofsMount{
		mountpoint: unit.Where,
		pipeRD:     pipeFDs[0],
		pipeWR:     pipeFDs[1],
		ioctlFD:    ioctlFD,
		timeout:    unit.Timeout,
		unit:       unit,
		mounted:    make(map[string]time.Time),
	}

	// Set idle timeout (in seconds) via ioctl
	if unit.Timeout > 0 {
		timeoutSecs := uint64(unit.Timeout / time.Second)
		if err := am.setTimeout(timeoutSecs); err != nil {
			am.Close()
			return nil, fmt.Errorf("set timeout: %w", err)
		}
	}

	// Close write end — the kernel already has it from mount()
	unix.Close(pipeFDs[1])
	am.pipeWR = -1

	return am, nil
}

// PipeFD returns the read-end pipe fd for epoll registration.
func (am *AutofsMount) PipeFD() int {
	return am.pipeRD
}

// Mountpoint returns the autofs mount point path.
func (am *AutofsMount) Mountpoint() string {
	return am.mountpoint
}

// Unit returns the mount unit configuration.
func (am *AutofsMount) Unit() *MountUnit {
	return am.unit
}

// Ready notifies the kernel that the mount request succeeded.
func (am *AutofsMount) Ready(token uint32) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL,
		uintptr(am.ioctlFD), AUTOFS_IOC_READY, uintptr(token))
	if errno != 0 {
		return fmt.Errorf("AUTOFS_IOC_READY: %w", errno)
	}
	return nil
}

// Fail notifies the kernel that the mount request failed.
func (am *AutofsMount) Fail(token uint32) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL,
		uintptr(am.ioctlFD), AUTOFS_IOC_FAIL, uintptr(token))
	if errno != 0 {
		return fmt.Errorf("AUTOFS_IOC_FAIL: %w", errno)
	}
	return nil
}

// ExpireMulti triggers kernel-side expiry of idle sub-mounts.
// Calls AUTOFS_IOC_EXPIRE_MULTI in a loop until EAGAIN.
// Returns the number of entries expired.
func (am *AutofsMount) ExpireMulti() (int, error) {
	expired := 0
	for {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL,
			uintptr(am.ioctlFD), AUTOFS_IOC_EXPIRE_MULTI, 0)
		if errno == unix.EAGAIN {
			break // no more entries to expire
		}
		if errno != 0 {
			return expired, fmt.Errorf("AUTOFS_IOC_EXPIRE_MULTI: %w", errno)
		}
		expired++
	}
	return expired, nil
}

// TrackMount records that a sub-mount was established.
func (am *AutofsMount) TrackMount(name string) {
	am.mu.Lock()
	am.mounted[name] = time.Now()
	am.mu.Unlock()
}

// TrackUnmount records that a sub-mount was removed.
func (am *AutofsMount) TrackUnmount(name string) {
	am.mu.Lock()
	delete(am.mounted, name)
	am.mu.Unlock()
}

// ActiveMounts returns the names of currently mounted sub-entries.
func (am *AutofsMount) ActiveMounts() []string {
	am.mu.Lock()
	defer am.mu.Unlock()
	names := make([]string, 0, len(am.mounted))
	for name := range am.mounted {
		names = append(names, name)
	}
	return names
}

// Close tears down the autofs mount: catatonic mode, unmount, close fds.
func (am *AutofsMount) Close() error {
	// Put autofs in catatonic mode (stop sending notifications)
	unix.Syscall(unix.SYS_IOCTL,
		uintptr(am.ioctlFD), AUTOFS_IOC_CATATONIC, 0)

	// Unmount any active sub-mounts
	am.mu.Lock()
	for name := range am.mounted {
		target := am.mountpoint + "/" + name
		unix.Unmount(target, unix.MNT_DETACH)
	}
	am.mounted = nil
	am.mu.Unlock()

	// Unmount the autofs filesystem itself
	var firstErr error
	if err := unix.Unmount(am.mountpoint, unix.MNT_DETACH); err != nil {
		firstErr = fmt.Errorf("unmount %s: %w", am.mountpoint, err)
	}

	// Close file descriptors
	if am.ioctlFD >= 0 {
		unix.Close(am.ioctlFD)
		am.ioctlFD = -1
	}
	if am.pipeRD >= 0 {
		unix.Close(am.pipeRD)
		am.pipeRD = -1
	}
	if am.pipeWR >= 0 {
		unix.Close(am.pipeWR)
		am.pipeWR = -1
	}

	return firstErr
}

// setTimeout sets the autofs idle timeout via ioctl.
func (am *AutofsMount) setTimeout(secs uint64) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL,
		uintptr(am.ioctlFD), AUTOFS_IOC_SETTIMEOUT,
		uintptr(unsafe.Pointer(&secs)))
	if errno != 0 {
		return errno
	}
	return nil
}
