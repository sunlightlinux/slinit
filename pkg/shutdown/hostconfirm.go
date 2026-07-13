package shutdown

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// ConfirmHostname implements the s6-linux-init `-i` interactive
// safeguard: the caller must type the local machine's short hostname
// before slinit proceeds with the shutdown/reboot. Aim is to catch
// the common footgun of an admin running `reboot` inside an SSH
// session and taking down the wrong host.
//
// The prompt goes to /dev/tty (NOT stdin) so a batch invocation with
// a piped stdin can't accidentally satisfy the check — /dev/tty is
// the controlling terminal, present only for interactive sessions.
// The comparison is case-insensitive and matches only the first DNS
// label, which is what an admin muscle-memorises.
//
// Return semantics:
//   - nil → operator confirmed, caller may proceed
//   - non-nil → aborted; caller must NOT execute the shutdown
//
// action is a short verb ("reboot", "halt", "poweroff") used in the
// prompt so the operator understands what they're confirming.
func ConfirmHostname(action string) error {
	return confirmHostname(action, openControllingTTY, resolveShortHostname)
}

// openControllingTTY is the default TTY-opener used by ConfirmHostname.
// Split out so tests can substitute a controlled reader/writer pair.
func openControllingTTY() (io.ReadCloser, io.Writer, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	return tty, tty, nil
}

// resolveShortHostname returns the machine's short hostname (first
// label). Kernel uses Uname().Nodename which is the same as `hostname
// -s` on Linux when the FQDN hasn't been baked into the kernel
// nodename — matching typical admin muscle memory.
func resolveShortHostname() (string, error) {
	var uts unix.Utsname
	if err := unix.Uname(&uts); err != nil {
		return "", fmt.Errorf("uname: %w", err)
	}
	name := unix.ByteSliceToString(uts.Nodename[:])
	if i := strings.IndexByte(name, '.'); i > 0 {
		name = name[:i]
	}
	return name, nil
}

// confirmHostname is the testable core. Isolates the injected
// dependencies so the happy-path, mismatch, no-tty, and eof paths
// can each be exercised without touching /dev/tty.
func confirmHostname(
	action string,
	openTTY func() (io.ReadCloser, io.Writer, error),
	getHostname func() (string, error),
) error {
	host, err := getHostname()
	if err != nil {
		return fmt.Errorf("cannot resolve local hostname: %w", err)
	}
	if host == "" {
		return fmt.Errorf("local hostname is empty; refusing to confirm")
	}

	rc, w, err := openTTY()
	if err != nil {
		// No /dev/tty — this is a batch invocation and the operator
		// explicitly asked for interactive confirmation. Fail closed:
		// they didn't get a chance to confirm, so we do NOT reboot.
		return fmt.Errorf("interactive confirmation requested but no controlling tty (%w)", err)
	}
	defer rc.Close()

	fmt.Fprintf(w, "About to %s. Type the short hostname to confirm: ", action)

	line, err := bufio.NewReader(rc).ReadString('\n')
	if err != nil && err != io.EOF {
		return fmt.Errorf("read confirmation: %w", err)
	}
	got := strings.TrimSpace(line)

	if !strings.EqualFold(got, host) {
		return fmt.Errorf(
			"hostname mismatch: typed %q, expected %q — aborting", got, host)
	}
	return nil
}
