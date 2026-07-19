package service

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Bucket A2 predicates — mid-complexity checks that inspect a specific
// filesystem/DMI/cgroup source. Each maps to one systemd ConditionXxx=
// directive. Kept in one file so their small helpers stay co-located.

// -------- file-is-executable --------------------------------------------

// checkFileIsExecutable succeeds when the path is a regular file with
// any exec bit set. Missing / non-regular / non-executable all fail.
// Systemd's ConditionFileIsExecutable= has the same semantics.
func checkFileIsExecutable(param string) (bool, string) {
	path := strings.TrimSpace(param)
	if path == "" {
		return false, "file-is-executable: empty path"
	}
	st, err := os.Stat(path)
	if err != nil {
		return false, fmt.Sprintf("file-is-executable: %v", err)
	}
	if !st.Mode().IsRegular() {
		return false, fmt.Sprintf("%q is not a regular file", path)
	}
	if st.Mode().Perm()&0111 == 0 {
		return false, fmt.Sprintf("%q has no execute bit set", path)
	}
	return true, ""
}

// -------- path-is-symbolic-link ------------------------------------------

// checkPathIsSymbolicLink uses lstat so the check reports on the link
// itself, not the target. Matches ConditionPathIsSymbolicLink=.
func checkPathIsSymbolicLink(param string) (bool, string) {
	path := strings.TrimSpace(param)
	if path == "" {
		return false, "path-is-symbolic-link: empty path"
	}
	st, err := os.Lstat(path)
	if err != nil {
		return false, fmt.Sprintf("path-is-symbolic-link: %v", err)
	}
	if st.Mode()&os.ModeSymlink == 0 {
		return false, fmt.Sprintf("%q is not a symbolic link", path)
	}
	return true, ""
}

// -------- path-is-read-write ---------------------------------------------

// checkPathIsReadWrite statfs's the path and inspects Flags for
// ST_RDONLY. A missing path is a failure. A read-only mount fails; a
// mount without the RDONLY flag succeeds. Systemd's
// ConditionPathIsReadWrite= has the same semantics.
func checkPathIsReadWrite(param string) (bool, string) {
	path := strings.TrimSpace(param)
	if path == "" {
		return false, "path-is-read-write: empty path"
	}
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return false, fmt.Sprintf("path-is-read-write: statfs %q: %v", path, err)
	}
	if st.Flags&unix.ST_RDONLY != 0 {
		return false, fmt.Sprintf("%q is mounted read-only", path)
	}
	return true, ""
}

// -------- firmware -------------------------------------------------------

// checkFirmware maps well-known firmware names to sysfs indicators.
// Matches the subset of ConditionFirmware= that's meaningful outside
// systemd's DMI-string lookup table.
//
//	uefi          → /sys/firmware/efi exists
//	bios          → /sys/class/dmi/id/bios_vendor is readable
//	device-tree   → /sys/firmware/devicetree exists
//	smbios        → /sys/firmware/dmi/tables exists
//
// Anything else falls back to a DMI product-name substring match against
// /sys/class/dmi/id/product_name (systemd calls this the "smbios(...)"
// syntax; we accept the raw string for simplicity).
func checkFirmware(param string) (bool, string) {
	name := strings.ToLower(strings.TrimSpace(param))
	switch name {
	case "uefi":
		if _, err := os.Stat("/sys/firmware/efi"); err == nil {
			return true, ""
		}
		return false, "no /sys/firmware/efi (not a UEFI boot)"
	case "bios":
		data, err := os.ReadFile("/sys/class/dmi/id/bios_vendor")
		if err != nil || len(strings.TrimSpace(string(data))) == 0 {
			return false, "no DMI bios_vendor (no legacy BIOS?)"
		}
		return true, ""
	case "device-tree":
		if _, err := os.Stat("/sys/firmware/devicetree"); err == nil {
			return true, ""
		}
		return false, "no /sys/firmware/devicetree"
	case "smbios":
		if _, err := os.Stat("/sys/firmware/dmi/tables"); err == nil {
			return true, ""
		}
		return false, "no /sys/firmware/dmi/tables"
	}
	// Fallback: product-name substring match.
	pn, err := os.ReadFile("/sys/class/dmi/id/product_name")
	if err != nil {
		return false, fmt.Sprintf("firmware: unknown key %q + no DMI product_name", name)
	}
	if strings.Contains(strings.ToLower(strings.TrimSpace(string(pn))), name) {
		return true, ""
	}
	return false, fmt.Sprintf("firmware: %q not found in product_name %q", name, strings.TrimSpace(string(pn)))
}

// -------- machine-tag ----------------------------------------------------

// checkMachineTag reads /etc/machine-info and looks for the requested
// tag in its TAGS= field. TAGS is a whitespace-separated list; the tag
// match is case-sensitive per systemd convention.
func checkMachineTag(param string) (bool, string) {
	want := strings.TrimSpace(param)
	if want == "" {
		return false, "machine-tag: empty tag"
	}
	f, err := os.Open("/etc/machine-info")
	if err != nil {
		return false, fmt.Sprintf("machine-tag: /etc/machine-info: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "TAGS=") {
			continue
		}
		val := strings.TrimPrefix(line, "TAGS=")
		// Strip matching quotes.
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') ||
				(val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		for _, tag := range strings.Fields(val) {
			if tag == want {
				return true, ""
			}
		}
		return false, fmt.Sprintf("machine-tag: %q not in TAGS=%q", want, val)
	}
	return false, "machine-tag: no TAGS= line in /etc/machine-info"
}

// -------- credential -----------------------------------------------------

// checkCredential succeeds when the named credential is available at
// $CREDENTIALS_DIRECTORY/<name>. Matches systemd's
// ConditionCredential=. The predicate is evaluated in the manager's
// environment; a service being started by slinit that declared its own
// credentials sees $CREDENTIALS_DIRECTORY pointing at
// /run/credentials/<svc>/, so this check is meaningful for services
// that need to gate on a specific credential having been provisioned.
func checkCredential(param string) (bool, string) {
	name := strings.TrimSpace(param)
	if name == "" {
		return false, "credential: empty name"
	}
	// systemd rejects '/' and NUL in the credential name; we do the
	// same so an operator can't escape the credentials dir.
	if strings.ContainsAny(name, "/\x00") {
		return false, "credential: name must not contain '/' or NUL"
	}
	dir := os.Getenv("CREDENTIALS_DIRECTORY")
	if dir == "" {
		return false, "credential: CREDENTIALS_DIRECTORY unset"
	}
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		return false, fmt.Sprintf("credential: %v", err)
	}
	return true, ""
}

// -------- control-group-controller ---------------------------------------

// checkControlGroupController succeeds when the named cgroup v2
// controller is available at the root — i.e. listed in
// /sys/fs/cgroup/cgroup.controllers. Matches
// ConditionControlGroupController=.
func checkControlGroupController(param string) (bool, string) {
	want := strings.TrimSpace(param)
	if want == "" {
		return false, "control-group-controller: empty name"
	}
	data, err := os.ReadFile("/sys/fs/cgroup/cgroup.controllers")
	if err != nil {
		return false, fmt.Sprintf("control-group-controller: /sys/fs/cgroup/cgroup.controllers: %v", err)
	}
	for _, c := range strings.Fields(string(data)) {
		if c == want {
			return true, ""
		}
	}
	return false, fmt.Sprintf("control-group-controller: %q not enabled at cgroup v2 root", want)
}
