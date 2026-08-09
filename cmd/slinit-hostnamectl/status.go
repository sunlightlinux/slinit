// status.go — collect + render the "hostnamectl status" output.
//
// Data sources (all on-disk, no D-Bus):
//   - kernel hostname: uname(2)
//   - static hostname: /etc/hostname
//   - machine-info fields: /etc/machine-info
//   - machine ID: /etc/machine-id
//   - boot ID: /proc/sys/kernel/random/boot_id
//   - kernel / arch: uname(2) release + machine
//   - OS pretty name: /etc/os-release PRETTY_NAME
//   - hardware vendor / model: /sys/class/dmi/id/{sys_vendor,product_name}
//   - firmware: /sys/class/dmi/id/bios_{vendor,version,date}
//   - chassis (auto): DMI chassis_type mapped to systemd's names
//   - virtualization: DMI + /proc hints; detectVirt.go handles it

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// statusFields is the payload for both text and JSON rendering.
// Field names in JSON tags match systemd's hostnamectl --json output
// so downstream tooling can consume either implementation.
type statusFields struct {
	StaticHostname    string `json:"StaticHostname,omitempty"`
	PrettyHostname    string `json:"PrettyHostname,omitempty"`
	TransientHostname string `json:"TransientHostname,omitempty"`
	Hostname          string `json:"Hostname,omitempty"`
	IconName          string `json:"IconName,omitempty"`
	Chassis           string `json:"Chassis,omitempty"`
	Deployment        string `json:"Deployment,omitempty"`
	Location          string `json:"Location,omitempty"`
	MachineID         string `json:"MachineID,omitempty"`
	BootID            string `json:"BootID,omitempty"`
	Virtualization    string `json:"Virtualization,omitempty"`
	OSPrettyName      string `json:"OperatingSystemPrettyName,omitempty"`
	OSCPEName         string `json:"OperatingSystemCPEName,omitempty"`
	OSHomeURL         string `json:"OperatingSystemHomeURL,omitempty"`
	OSSupportEnd      string `json:"OperatingSystemSupportEnd,omitempty"`
	KernelName        string `json:"KernelName,omitempty"`
	KernelRelease     string `json:"KernelRelease,omitempty"`
	KernelVersion     string `json:"KernelVersion,omitempty"`
	Architecture      string `json:"Architecture,omitempty"`
	HardwareVendor    string `json:"HardwareVendor,omitempty"`
	HardwareModel     string `json:"HardwareModel,omitempty"`
	FirmwareVersion   string `json:"FirmwareVersion,omitempty"`
	FirmwareVendor    string `json:"FirmwareVendor,omitempty"`
	FirmwareDate      string `json:"FirmwareDate,omitempty"`
}

// runStatus collects the fields and renders them per --json / text.
// A hostname arg after "status" is a systemd quirk (`hostnamectl status
// NAME` is invalid); we ignore extras rather than erroring.
func runStatus(out io.Writer, opts options) error {
	s := collectStatus()
	if opts.jsonMode != "" && opts.jsonMode != "off" {
		return renderJSON(out, s, opts.jsonMode)
	}
	return renderText(out, s)
}

func collectStatus() statusFields {
	var s statusFields
	mi, _ := loadMachineInfo()

	s.StaticHostname = readTrimmed(hostnamePath)
	if mi != nil {
		s.PrettyHostname = mi.get("PRETTY_HOSTNAME")
		s.IconName = mi.get("ICON_NAME")
		s.Chassis = mi.get("CHASSIS")
		s.Deployment = mi.get("DEPLOYMENT")
		s.Location = mi.get("LOCATION")
		s.HardwareVendor = mi.get("HARDWARE_VENDOR")
		s.HardwareModel = mi.get("HARDWARE_MODEL")
		s.FirmwareVersion = mi.get("FIRMWARE_VERSION")
		s.FirmwareVendor = mi.get("FIRMWARE_VENDOR")
		s.FirmwareDate = mi.get("FIRMWARE_DATE")
	}

	var uname unix.Utsname
	if unix.Uname(&uname) == nil {
		s.KernelName = cstring(uname.Sysname[:])
		s.KernelRelease = cstring(uname.Release[:])
		s.KernelVersion = cstring(uname.Version[:])
		s.Architecture = cstring(uname.Machine[:])
		s.Hostname = cstring(uname.Nodename[:])
	}
	s.TransientHostname = transientDisplay(s.Hostname, s.StaticHostname)

	s.MachineID = readTrimmed(machineIDPath)
	s.BootID = readTrimmed(bootIDPath)

	if osrel, err := parseOSRelease(osReleasePath); err == nil {
		s.OSPrettyName = osrel["PRETTY_NAME"]
		s.OSCPEName = osrel["CPE_NAME"]
		s.OSHomeURL = osrel["HOME_URL"]
		s.OSSupportEnd = osrel["SUPPORT_END"]
	}

	if s.HardwareVendor == "" {
		s.HardwareVendor = readTrimmed(dmiSysVendorPath)
	}
	if s.HardwareModel == "" {
		s.HardwareModel = readTrimmed(dmiProductNamePath)
	}
	if s.FirmwareVendor == "" {
		s.FirmwareVendor = readTrimmed(dmiBiosVendorPath)
	}
	if s.FirmwareVersion == "" {
		s.FirmwareVersion = readTrimmed(dmiBiosVersionPath)
	}
	if s.FirmwareDate == "" {
		s.FirmwareDate = readTrimmed(dmiBiosDatePath)
	}

	// Chassis: honor operator override; else auto-detect.
	if s.Chassis == "" {
		s.Chassis = detectChassis()
	}
	s.Virtualization = detectVirt()

	return s
}

// renderText mimics systemd's two-column "Key: Value" layout. Empty
// fields are skipped (matches systemd's presentation for unpopulated
// machine-info entries).
func renderText(out io.Writer, s statusFields) error {
	rows := []struct{ k, v string }{
		{"Static hostname", s.StaticHostname},
		{"Transient hostname", s.TransientHostname},
		{"Pretty hostname", s.PrettyHostname},
		{"Icon name", s.IconName},
		{"Chassis", s.Chassis},
		{"Deployment", s.Deployment},
		{"Location", s.Location},
		{"Machine ID", s.MachineID},
		{"Boot ID", s.BootID},
		{"Virtualization", s.Virtualization},
		{"Operating System", s.OSPrettyName},
		{"CPE OS Name", s.OSCPEName},
		{"OS Support End", s.OSSupportEnd},
		{"Kernel", joinKernel(s.KernelName, s.KernelRelease)},
		{"Architecture", s.Architecture},
		{"Hardware Vendor", s.HardwareVendor},
		{"Hardware Model", s.HardwareModel},
		{"Firmware Version", s.FirmwareVersion},
		{"Firmware Vendor", s.FirmwareVendor},
		{"Firmware Date", s.FirmwareDate},
	}
	for _, r := range rows {
		if r.v == "" {
			continue
		}
		fmt.Fprintf(out, "%20s: %s\n", r.k, r.v)
	}
	return nil
}

func renderJSON(out io.Writer, s statusFields, mode string) error {
	enc := json.NewEncoder(out)
	if mode == "pretty" {
		enc.SetIndent("", "    ")
	}
	return enc.Encode(s)
}

// transientDisplay returns the value we want to show for
// "Transient hostname" in status output.
//
// The kernel's nodename is only interesting when it differs from the
// on-disk static hostname AND names a real host — the values `(none)`
// (Linux's placeholder when nothing has ever called sethostname(2))
// and `localhost` (distro default before any hostname config lands)
// are both "hostname unset" sentinels and match what
// systemd-hostnamed hides from operators. See core/hostname-setup.c
// in systemd for the same list.
func transientDisplay(kernel, static string) string {
	if kernel == "" {
		return ""
	}
	if kernel == static {
		return ""
	}
	switch kernel {
	case "(none)", "localhost", "localhost.localdomain":
		return ""
	}
	return kernel
}

func joinKernel(name, release string) string {
	name = strings.TrimSpace(name)
	release = strings.TrimSpace(release)
	switch {
	case name != "" && release != "":
		return name + " " + release
	case name != "":
		return name
	case release != "":
		return release
	}
	return ""
}

// cstring turns a fixed-size C-string (Utsname fields) into a Go
// string by cutting at the first NUL.
func cstring(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// readTrimmed reads a file and returns its whitespace-trimmed
// contents, or "" if the file is missing / unreadable. Used liberally
// for /proc, /sys, and small config files.
func readTrimmed(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// parseOSRelease is a tiny os-release(5) reader. Reuses the
// machine-info decoder since the grammar is identical.
func parseOSRelease(path string) (map[string]string, error) {
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

// Paths overridable in tests. `hostnamePath` is also used by set.go.
var (
	hostnamePath       = "/etc/hostname"
	machineIDPath      = "/etc/machine-id"
	bootIDPath         = "/proc/sys/kernel/random/boot_id"
	osReleasePath      = "/etc/os-release"
	dmiSysVendorPath   = "/sys/class/dmi/id/sys_vendor"
	dmiProductNamePath = "/sys/class/dmi/id/product_name"
	dmiChassisTypePath = "/sys/class/dmi/id/chassis_type"
	dmiBiosVendorPath  = "/sys/class/dmi/id/bios_vendor"
	dmiBiosVersionPath = "/sys/class/dmi/id/bios_version"
	dmiBiosDatePath    = "/sys/class/dmi/id/bios_date"
)
