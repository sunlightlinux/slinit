// Package hostname holds what slinit-hostnamectl and slinit-hostnamed
// share: /etc/hostname, /etc/machine-info, os-release, the DMI fields and
// chassis and virtualization detection. The paths are variables so tests
// can point them at a scratch tree.
package hostname

import (
	"os"
	"strings"
)

// ReadTrimmed reads a file and returns its whitespace-trimmed
// contents, or "" if the file is missing / unreadable. Used liberally
// for /proc, /sys, and small config files.
func ReadTrimmed(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ParseOSRelease is a tiny os-release(5) reader. Reuses the
// machine-info decoder since the grammar is identical.
func ParseOSRelease(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		out[strings.TrimSpace(line[:eq])] = decodeValue(strings.TrimSpace(line[eq+1:]))
	}
	return out, nil
}

// Paths overridable in tests. `HostnamePath` is also used by set.go.
var (
	HostnamePath       = "/etc/hostname"
	MachineIDPath      = "/etc/machine-id"
	BootIDPath         = "/proc/sys/kernel/random/boot_id"
	OSReleasePath      = "/etc/os-release"
	DMISysVendorPath   = "/sys/class/dmi/id/sys_vendor"
	DMIProductNamePath = "/sys/class/dmi/id/product_name"
	DMIChassisTypePath = "/sys/class/dmi/id/chassis_type"
	DMIBiosVendorPath  = "/sys/class/dmi/id/bios_vendor"
	DMIBiosVersionPath = "/sys/class/dmi/id/bios_version"
	DMIBiosDatePath    = "/sys/class/dmi/id/bios_date"
)

// ValidChassis mirrors systemd's accepted set.
func ValidChassis(v string) bool {
	switch v {
	case "desktop", "laptop", "convertible", "server", "tablet",
		"handset", "watch", "embedded", "vm", "container":
		return true
	}
	return false
}

// WriteAtomic writes buf to path via tmp+rename in the same dir so
// concurrent readers never see a partial file.
func WriteAtomic(path string, buf []byte, mode os.FileMode) error {
	dir := "."
	if idx := strings.LastIndex(path, "/"); idx > 0 {
		dir = path[:idx]
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".hostnamectl.")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
