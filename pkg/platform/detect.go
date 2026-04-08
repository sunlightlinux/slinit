// Package platform detects the virtualization/container environment in which
// slinit is running.  Services can declare `keyword -docker -lxc ...` to be
// automatically skipped on platforms where they cannot function (e.g. hardware
// services inside containers).
//
// The detection logic mirrors OpenRC's rc_sys / detect_container / detect_vm
// approach, checking well-known files and /proc entries.
package platform

import (
	"os"
	"strings"
)

// Type represents a detected platform/virtualization environment.
type Type string

const (
	None          Type = ""              // bare metal / unknown
	Docker        Type = "docker"        // Docker container
	Podman        Type = "podman"        // Podman container
	LXC           Type = "lxc"          // Linux Containers
	SystemdNspawn Type = "systemd-nspawn" // systemd-nspawn container
	OpenVZ        Type = "openvz"        // OpenVZ container
	Vserver       Type = "vserver"       // Linux VServer
	RKT           Type = "rkt"          // CoreOS rkt
	UML           Type = "uml"          // User-Mode Linux
	WSL           Type = "wsl"          // Windows Subsystem for Linux
	Xen0          Type = "xen0"         // Xen Dom0 (control domain)
	XenU          Type = "xenu"         // Xen DomU (guest domain)
)

// AllTypes returns all known platform types for validation.
func AllTypes() []Type {
	return []Type{
		Docker, Podman, LXC, SystemdNspawn, OpenVZ,
		Vserver, RKT, UML, WSL, Xen0, XenU,
	}
}

// IsValid returns true if the given string is a known platform type.
func IsValid(s string) bool {
	for _, t := range AllTypes() {
		if strings.EqualFold(s, string(t)) {
			return true
		}
	}
	return s == "" || strings.EqualFold(s, "none")
}

// Detect auto-detects the current platform by checking well-known files
// and /proc entries.  Container detection runs before VM detection,
// matching OpenRC's priority order.
func Detect() Type {
	// Container detection (order matters — more specific checks first)
	if t := detectContainer(); t != None {
		return t
	}
	// VM detection
	return detectVM()
}

// readFileFunc is mockable for testing.
var readFileFunc = os.ReadFile
var statFunc = os.Stat

func detectContainer() Type {
	// UML: /proc/cpuinfo contains "UML"
	if cpuinfo, err := readFileFunc("/proc/cpuinfo"); err == nil {
		if strings.Contains(string(cpuinfo), "UML") {
			return UML
		}
	}

	// VServer: /proc/self/status contains "VxID" or "s_context" with non-zero value
	if status, err := readFileFunc("/proc/self/status"); err == nil {
		s := string(status)
		for _, line := range strings.Split(s, "\n") {
			if strings.HasPrefix(line, "VxID:") || strings.HasPrefix(line, "s_context:") {
				parts := strings.Fields(line)
				if len(parts) >= 2 && parts[1] != "0" {
					return Vserver
				}
			}
		}
	}

	// OpenVZ: /proc/vz/veinfo exists but /proc/vz/version does not
	if _, err := statFunc("/proc/vz/veinfo"); err == nil {
		if _, err := statFunc("/proc/vz/version"); err != nil {
			return OpenVZ
		}
	}

	// Check /proc/1/environ for container= marker
	// (LXC, RKT, systemd-nspawn, Docker all set this)
	if environ, err := readFileFunc("/proc/1/environ"); err == nil {
		env := string(environ)
		// /proc/1/environ uses NUL as separator
		for _, entry := range strings.Split(env, "\x00") {
			switch {
			case entry == "container=lxc":
				return LXC
			case entry == "container=rkt":
				return RKT
			case entry == "container=systemd-nspawn":
				return SystemdNspawn
			case entry == "container=docker":
				return Docker
			case entry == "container=podman":
				return Podman
			}
		}
	}

	// Podman: /run/.containerenv exists
	if _, err := statFunc("/run/.containerenv"); err == nil {
		return Podman
	}

	// Docker: /.dockerenv exists
	if _, err := statFunc("/.dockerenv"); err == nil {
		return Docker
	}

	// WSL: /proc/sys/kernel/osrelease contains "microsoft" (case-insensitive)
	if osrel, err := readFileFunc("/proc/sys/kernel/osrelease"); err == nil {
		if strings.Contains(strings.ToLower(string(osrel)), "microsoft") {
			return WSL
		}
	}
	// WSL: WSL_DISTRO_NAME env var
	if os.Getenv("WSL_DISTRO_NAME") != "" {
		return WSL
	}
	// WSL: /proc/sys/fs/binfmt_misc/WSLInterop exists
	if _, err := statFunc("/proc/sys/fs/binfmt_misc/WSLInterop"); err == nil {
		return WSL
	}

	return None
}

func detectVM() Type {
	// Xen: /proc/xen exists
	if fi, err := statFunc("/proc/xen"); err == nil && fi.IsDir() {
		// Dom0 has "control_d" in /proc/xen/capabilities
		if caps, err := readFileFunc("/proc/xen/capabilities"); err == nil {
			if strings.Contains(string(caps), "control_d") {
				return Xen0
			}
		}
		return XenU
	}

	return None
}

// MatchesKeyword checks if a keyword string (e.g. "-docker", "-lxc") matches
// the detected platform.  Keywords use the "-platform" convention from OpenRC.
func MatchesKeyword(keyword string, detected Type) bool {
	if detected == None {
		return false
	}
	// Strip leading "-" prefix
	kw := strings.TrimPrefix(keyword, "-")
	// Also support "noplatform" form (OpenRC compat)
	kw = strings.TrimPrefix(kw, "no")

	return strings.EqualFold(kw, string(detected))
}

// ShouldSkip checks whether a service with the given keywords should be
// skipped on the detected platform.  Returns true if any keyword matches.
func ShouldSkip(keywords []string, detected Type) bool {
	if detected == None {
		return false
	}
	for _, kw := range keywords {
		if MatchesKeyword(kw, detected) {
			return true
		}
	}
	return false
}
