// Package network wires the two Debian/BusyBox-compat network
// hooks slinit needs to match finit's out-of-the-box behaviour:
//
//   - BringUpLoopback: set IFF_UP on the "lo" interface via
//     SIOCSIFFLAGS at very early boot, before services start.
//     Many daemons (postgresql, redis, sshd LoginGraceTime) hang
//     or refuse to start when loopback is down; init is the only
//     place with the guarantee + privilege to do it before them.
//     Zero external dependency — the kernel populates 127.0.0.1/8
//     automatically for lo, we just need to flip the up bit.
//
//   - RunIfup: fork+exec `ifup -a` (or `ifdown -a --force` /
//     `ifdown -a -f` at shutdown) if /etc/network/interfaces
//     exists AND an ifup binary is on PATH. finit-parity for the
//     zero-config Debian/BusyBox integration — an operator who
//     ships /etc/network/interfaces today gets network up on
//     first slinit boot without writing a service.
//
// Both hooks are best-effort: failures are logged but don't gate
// boot / shutdown. Missing prerequisites (no ifup on PATH, no
// interfaces file, loopback ioctl unavailable) fall through
// silently. Matches finit's `void networking(int updown)` in
// src/helpers.c:522-597.
package network

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"golang.org/x/sys/unix"
)

// ifupTimeout caps how long an `ifup -a` invocation may run.
// Debian's ifupdown often calls dhclient with a 60 s default
// retry window; a 90 s cap gives one full retry cycle + margin
// without letting a wedged network config stall boot forever.
const ifupTimeout = 90 * time.Second

// interfacesPath is the well-known Debian/BusyBox network
// interfaces file. Overridable for tests.
var interfacesPath = "/etc/network/interfaces"

// ifupBins is the list of executables slinit will fork for
// bring-up, in preference order. `ifup` covers both Debian's
// ifupdown package and BusyBox's ifupdown applet. Extendable
// via SetIfupBins for niche embedded builds that ship the tool
// under a different name.
var ifupBins = []string{"ifup"}

// SetInterfacesPath overrides the /etc/network/interfaces path.
func SetInterfacesPath(p string) { interfacesPath = p }

// SetIfupBins overrides the ifup executable-preference list.
func SetIfupBins(bins []string) { ifupBins = bins }

// BringUpLoopback sets IFF_UP on the "lo" interface via
// SIOCSIFFLAGS ioctl. Idempotent: if lo is already up, the read
// shows IFF_UP already set and the write is a no-op — no error
// returned. Any error opening the socket or issuing the ioctl is
// returned to the caller; init's usual policy is to log and
// continue.
func BringUpLoopback() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return fmt.Errorf("loopback: socket: %w", err)
	}
	defer unix.Close(fd)

	ifr, err := unix.NewIfreq("lo")
	if err != nil {
		return fmt.Errorf("loopback: ifreq: %w", err)
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return fmt.Errorf("loopback: SIOCGIFFLAGS: %w", err)
	}
	flags := ifr.Uint16()
	if flags&unix.IFF_UP != 0 {
		return nil // already up, no work
	}
	ifr.SetUint16(flags | unix.IFF_UP | unix.IFF_RUNNING)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr); err != nil {
		return fmt.Errorf("loopback: SIOCSIFFLAGS: %w", err)
	}
	return nil
}

// RunIfup fires the network configuration wrapper. `up == true`
// brings interfaces up (`ifup -a`); `up == false` brings them
// down (`ifdown -a --force` when ifquery is available, else
// `ifdown -a -f` for BusyBox). Returns nil silently when either
// prerequisite is missing (no interfaces file, no ifup on PATH)
// so operators who don't use Debian-style network config aren't
// spammed with warnings.
func RunIfup(up bool, logger *logging.Logger) error {
	if _, err := os.Stat(interfacesPath); err != nil {
		return nil // no config file → no-op
	}
	ifupBin := findOnPath(ifupBins)
	if ifupBin == "" {
		return nil // no ifup on PATH → no-op (loopback fallback happens elsewhere)
	}
	var args []string
	tool := ifupBin
	if up {
		args = []string{"-a"}
	} else {
		// finit-parity: distinguish Debian ifupdown (has ifquery,
		// takes --force) from BusyBox (no ifquery, takes -f).
		toolIfdown := findIfdown()
		if toolIfdown == "" {
			return nil
		}
		tool = toolIfdown
		if findOnPath([]string{"ifquery"}) != "" {
			args = []string{"-a", "--force"}
		} else {
			args = []string{"-a", "-f"}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), ifupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, tool, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("ifup: stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout // merge; ifupdown writes both channels
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ifup: start %s: %w", tool, err)
	}
	// finit-parity: log each output line prefixed "network:" so
	// operators can grep the log stream.
	go pipeLines(stdout, logger)
	logger.Info("network: running %s %v", tool, args)
	if err := cmd.Wait(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("ifup: timeout after %v", ifupTimeout)
		}
		return fmt.Errorf("ifup: %s exited %v", tool, err)
	}
	return nil
}

// findOnPath returns the first bin in the preference list that
// resolves on PATH, or "" when none does.
func findOnPath(bins []string) string {
	for _, b := range bins {
		if p, err := exec.LookPath(b); err == nil {
			return p
		}
	}
	return ""
}

// findIfdown finds the ifdown executable. Debian and BusyBox both
// ship it alongside ifup, but a stripped-down image might carry
// only ifup — in which case we can't do a clean teardown and
// return "" so the caller becomes a no-op.
func findIfdown() string {
	if p, err := exec.LookPath("ifdown"); err == nil {
		return p
	}
	return ""
}

// pipeLines reads r line-by-line and forwards each to the logger
// with the "network:" prefix used by finit.
func pipeLines(r io.Reader, logger *logging.Logger) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		logger.Info("network: %s", sc.Text())
	}
}
