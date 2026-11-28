package service

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/sunlightlinux/slinit/pkg/process"
	"golang.org/x/sys/unix"
)

// Host probes behind several predicates, each a port of the systemd
// helper its Condition*= calls, so the answers agree.

// -------- security=audit --------------------------------------------------

// auditProbe opens a NETLINK_AUDIT socket and sends AUDIT_GET_FEATURE,
// returning the error of each step (reqErr is not looked at when
// sockErr is set). Tests replace it.
var auditProbe = probeAuditNetlink

func probeAuditNetlink() (sockErr, reqErr error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, unix.NETLINK_AUDIT)
	if err != nil {
		return err, nil
	}
	defer unix.Close(fd)
	// struct nlmsghdr, no payload: len, type, flags, seq, pid, in host
	// byte order as netlink has it.
	ne := binary.NativeEndian
	req := make([]byte, unix.NLMSG_HDRLEN)
	ne.PutUint32(req[0:], unix.NLMSG_HDRLEN)
	ne.PutUint16(req[4:], unix.AUDIT_GET_FEATURE)
	ne.PutUint16(req[6:], unix.NLM_F_REQUEST|unix.NLM_F_ACK)
	// An unconnected netlink socket sends to the kernel (port 0).
	if err := unix.Sendto(fd, req, unix.MSG_NOSIGNAL, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return nil, err
	}
	// The kernel answers inside sendmsg, so the ack is queued already;
	// a non-blocking read cannot hang the service set's lock.
	buf := make([]byte, 64)
	n, _, err := unix.Recvfrom(fd, buf, 0)
	if err != nil {
		return nil, err
	}
	// nlmsghdr + struct nlmsgerr (int error + the request's header).
	if n != unix.NLMSG_HDRLEN+4+unix.NLMSG_HDRLEN {
		return nil, unix.EIO
	}
	if ne.Uint16(buf[4:]) != unix.NLMSG_ERROR {
		return nil, unix.EINVAL
	}
	code := int32(ne.Uint32(buf[16:]))
	if code < 0 {
		return nil, syscall.Errno(-code)
	}
	return nil, nil
}

// auditUsable is the decision of systemd's use_audit (libaudit-util.c)
// on the probe's errors: audit is off only when the kernel has no audit
// (a not-supported errno), the caller may not open the socket (a
// privilege errno), or the kernel refuses audit outside the initial
// user namespace (ECONNREFUSED on the request). Any other failure leaves
// it on, because it does not show that audit will not work. (systemd
// also answers no when libaudit.so.1 cannot be loaded; that is about
// systemd's own logging and has no slinit counterpart.)
func auditUsable(sockErr, reqErr error) bool {
	if sockErr != nil {
		var e syscall.Errno
		if !errors.As(sockErr, &e) {
			return true
		}
		switch e {
		case unix.EACCES, unix.EPERM, // ERRNO_IS_PRIVILEGE
			unix.EOPNOTSUPP, unix.ENOTTY, unix.ENOSYS, unix.EAFNOSUPPORT, unix.EPFNOSUPPORT,
			unix.EPROTONOSUPPORT, unix.ESOCKTNOSUPPORT, unix.ENOPROTOOPT: // ERRNO_IS_NOT_SUPPORTED
			return false
		}
		return true
	}
	return !errors.Is(reqErr, unix.ECONNREFUSED)
}

// -------- kernel command line booleans --------------------------------

// cmdlineBool is proc_cmdline_get_bool: the last key or key=value word
// on /proc/cmdline for key ('-' and '_' alike in the key), a bare key
// being true. found is false when the key is absent or its value is
// not a boolean (systemd logs that and ignores the word).
func cmdlineBool(key string) (val, found bool) {
	data, err := os.ReadFile(hostPath("/proc/cmdline"))
	if err != nil {
		return false, false
	}
	norm := func(s string) string { return strings.ReplaceAll(s, "-", "_") }
	key = norm(key)
	var raw string
	have, bare := false, false
	for _, w := range strings.Fields(string(data)) {
		k, v, hasVal := strings.Cut(w, "=")
		if norm(k) != key {
			continue
		}
		if hasVal {
			raw, have, bare = v, true, false
		} else if !have {
			// A bare word sets nothing when a value came before it.
			bare = true
		}
	}
	if have {
		return parseSystemdBool(raw)
	}
	return bare, bare
}

// parseSystemdBool is systemd's parse_boolean.
func parseSystemdBool(v string) (val, ok bool) {
	switch strings.ToLower(v) {
	case "1", "yes", "y", "true", "t", "on":
		return true, true
	case "0", "no", "n", "false", "f", "off":
		return false, true
	}
	return false, false
}

// -------- security=measured-os ------------------------------------------

// measuredOS is systemd's efi_measured_os: whether the OS should
// measure itself into the TPM. $SYSTEMD_FORCE_MEASURE and then the
// systemd.tpm2_measured_os= kernel argument decide when set; otherwise
// it follows measured-uki, i.e. whether a UKI's stub measured the
// kernel. A TPM event log alone is not it: any TPM-equipped firmware
// writes one, booted from a UKI or not, and slinit took that for a
// measured OS.
func measuredOS() (bool, string) {
	if v, ok := os.LookupEnv("SYSTEMD_FORCE_MEASURE"); ok {
		if b, ok := parseSystemdBool(v); ok {
			if b {
				return true, ""
			}
			return false, "SYSTEMD_FORCE_MEASURE is false"
		}
	}
	if b, ok := cmdlineBool("systemd.tpm2_measured_os"); ok {
		if b {
			return true, ""
		}
		return false, "systemd.tpm2_measured_os= is false on the kernel command line"
	}
	return measuredUKI()
}

// measuredUKI: the sd-stub StubPcrKernelImage EFI variable exists and
// the TPM event log is not empty.
func measuredUKI() (bool, string) {
	const stubPCRVar = "/sys/firmware/efi/efivars/" +
		"StubPcrKernelImage-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f"
	if _, err := os.Stat(hostPath(stubPCRVar)); err != nil {
		return false, "no StubPcrKernelImage EFI variable (not booted via sd-stub UKI)"
	}
	// If the log is empty the UKI did not actually reach the TPM.
	if st, err := os.Stat(hostPath("/sys/kernel/security/tpm0/binary_bios_measurements")); err != nil {
		return false, "no TPM binary_bios_measurements log"
	} else if st.Size() == 0 {
		return false, "TPM binary_bios_measurements log is empty"
	}
	return true, ""
}

// -------- needs-update=DIR ------------------------------------------------

// needsUpdateDir is systemd's condition_test_needs_update for an
// absolute directory: true when /usr was modified after DIR/.updated,
// so the update tools run once after /usr changed. Its failures lean to
// true, as systemd's do, so that an update tool runs rather than not.
func needsUpdateDir(dir string) (bool, string) {
	if b, ok := cmdlineBool("systemd.condition_needs_update"); ok {
		if b {
			return true, ""
		}
		return false, "systemd.condition_needs_update= is false on the kernel command line"
	}
	if hostExists("/etc/initrd-release") {
		return false, "in the initrd, no updates"
	}
	var sfs unix.Statfs_t
	if err := unix.Statfs(hostPath(dir), &sfs); err == nil && sfs.Flags&unix.ST_RDONLY != 0 {
		return false, fmt.Sprintf("%s is on a read-only filesystem", dir)
	}
	stamp := hostPath(filepath.Join(dir, ".updated"))
	var other, usr unix.Stat_t
	if err := unix.Lstat(stamp, &other); err != nil {
		return true, ""
	}
	if err := unix.Lstat(hostPath("/usr/"), &usr); err != nil {
		return true, ""
	}
	newer := func() (bool, string) {
		return false, fmt.Sprintf("/usr is not newer than %s/.updated", dir)
	}
	if usr.Mtim.Sec != other.Mtim.Sec {
		if usr.Mtim.Sec > other.Mtim.Sec {
			return true, ""
		}
		return newer()
	}
	// Same second: the nanoseconds decide, unless the stamp's are 0
	// while /usr's are not, which means the filesystem holding the
	// stamp keeps no nanoseconds; then the stamp's TIMESTAMP_NSEC=.
	if usr.Mtim.Nsec == 0 || other.Mtim.Nsec > 0 {
		if usr.Mtim.Nsec > other.Mtim.Nsec {
			return true, ""
		}
		return newer()
	}
	data, err := os.ReadFile(stamp)
	if err != nil {
		return true, ""
	}
	var ts string
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "TIMESTAMP_NSEC="); ok {
			ts = strings.Trim(strings.TrimSpace(v), "\"'")
		}
	}
	n, err := strconv.ParseUint(ts, 10, 64)
	if err != nil {
		return true, ""
	}
	if uint64(usr.Mtim.Sec)*1e9+uint64(usr.Mtim.Nsec) > n {
		return true, ""
	}
	return newer()
}

// -------- directory-not-empty ---------------------------------------------

// hiddenOrBackupFile is systemd's hidden_or_backup_file.
func hiddenOrBackupFile(name string) bool {
	if strings.HasPrefix(name, ".") || name == "lost+found" || name == "aquota.user" ||
		name == "aquota.group" || strings.HasSuffix(name, "~") {
		return true
	}
	dot := strings.LastIndexByte(name, '.')
	if dot < 0 {
		return false
	}
	switch name[dot+1:] {
	case "ignore", "rpmnew", "rpmsave", "rpmorig", "dpkg-old", "dpkg-new", "dpkg-tmp",
		"dpkg-dist", "dpkg-bak", "dpkg-backup", "dpkg-remove", "ucf-new", "ucf-old",
		"ucf-dist", "swp", "bak", "old", "new":
		return true
	}
	return false
}

// checkDirectoryNotEmpty is systemd's condition_test_directory_not_empty:
// dir_is_empty(path, ignore_hidden_or_backup=true), so a directory that
// holds only dotfiles, editor backups or package-manager leftovers
// (foo.rpmnew, foo.dpkg-old) is empty; and a directory that cannot be
// read for any reason but its absence (ENOENT) or not being one
// (ENOTDIR) counts as not empty, since it cannot be shown empty.
func checkDirectoryNotEmpty(path string) (bool, string) {
	// O_DIRECTORY, as systemd: anything but a directory fails at once
	// with ENOTDIR, where a plain open of a FIFO would block the
	// service set's lock until a writer came.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err == nil {
		f := os.NewFile(uintptr(fd), path)
		defer f.Close()
		for err == nil {
			var names []string
			names, err = f.Readdirnames(64)
			for _, n := range names {
				if !hiddenOrBackupFile(n) {
					return true, ""
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return false, fmt.Sprintf("dir %q is empty", path)
		}
	}
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
		return false, fmt.Sprintf("dir %q: %v", path, err)
	}
	return true, ""
}

// checkCapability answers condition-capability: is the capability in
// slinit's bounding set (CapBnd in /proc/self/status)?
func checkCapability(name string) (bool, string) {
	caps, err := process.ParseCapabilities(name)
	if err != nil || len(caps) != 1 {
		return false, fmt.Sprintf("%q is not one capability name", name)
	}
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false, fmt.Sprintf("cannot read the bounding set: %v", err)
	}
	in, err := capInBoundingSet(string(b), caps[0])
	if err != nil {
		return false, err.Error()
	}
	if !in {
		return false, fmt.Sprintf("%s is not in slinit's capability bounding set", strings.ToUpper(name))
	}
	return true, ""
}

// capInBoundingSet reads CapBnd from a /proc/PID/status text.
func capInBoundingSet(status string, c uintptr) (bool, error) {
	for _, line := range strings.Split(status, "\n") {
		if v, ok := strings.CutPrefix(line, "CapBnd:"); ok {
			mask, err := strconv.ParseUint(strings.TrimSpace(v), 16, 64)
			if err != nil {
				return false, fmt.Errorf("bad CapBnd %q", v)
			}
			return c < 64 && mask&(1<<c) != 0, nil
		}
	}
	return false, errors.New("no CapBnd in the status")
}
