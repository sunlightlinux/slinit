package control

import (
	"net"
	"syscall"
)

// peerUID returns the effective UID of the peer connected via a Unix socket.
// The second return value is false if the credential could not be retrieved
// (e.g. the connection is not a Unix socket or the kernel didn't return
// peer credentials). Callers must treat (false) as untrusted.
func peerUID(c net.Conn) (uint32, bool) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var (
		ucred  *syscall.Ucred
		gerr   error
	)
	if cerr := raw.Control(func(fd uintptr) {
		ucred, gerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); cerr != nil {
		return 0, false
	}
	if gerr != nil || ucred == nil {
		return 0, false
	}
	return ucred.Uid, true
}
