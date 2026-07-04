// Package config implements the dinit-compatible service configuration file parser.
//
// initd.go provides auto-detection of /etc/init.d scripts as scripted services.
// It parses LSB init info headers to extract dependencies and metadata.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// LSBInfo holds parsed LSB init info headers from an init.d script.
type LSBInfo struct {
	Provides         []string // service names this script provides
	RequiredStart    []string // services/facilities required before start
	RequiredStop     []string // services/facilities required before stop
	ShouldStart      []string // optional dependencies for start
	ShouldStop       []string // optional dependencies for stop
	DefaultStart     []string // runlevels where service starts (e.g., "2 3 4 5")
	DefaultStop      []string // runlevels where service stops (e.g., "0 1 6")
	ShortDescription string   // one-line description
	Description      string   // multi-line description
}

// LSB virtual facility mapping to slinit service names.
// Unknown facilities are passed through as-is (the loader will
// resolve them or skip gracefully if not found).
var LSBFacilityMap = map[string]string{
	"$syslog":    "syslog",
	"$network":   "network",
	"$remote_fs": "remote-fs",
	"$local_fs":  "local-fs",
	"$time":      "time-sync",
	"$named":     "named",
	"$portmap":   "portmap",
}

// ParseLSBHeaders reads an init.d script and extracts LSB init info.
// Returns nil if no LSB headers are found (script is still usable, just without deps).
func ParseLSBHeaders(filePath string) (*LSBInfo, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var info LSBInfo
	inBlock := false
	var descLines []string

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()

		if strings.Contains(line, "### BEGIN INIT INFO") {
			inBlock = true
			continue
		}
		if strings.Contains(line, "### END INIT INFO") {
			break
		}
		if !inBlock {
			continue
		}

		// Strip leading "# " prefix
		line = strings.TrimPrefix(line, "#")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Parse "Key: value" format
		idx := strings.Index(line, ":")
		if idx < 0 {
			// Continuation of multi-line Description
			if len(descLines) > 0 {
				descLines = append(descLines, line)
			}
			continue
		}

		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])

		switch key {
		case "Provides":
			info.Provides = splitFields(value)
		case "Required-Start":
			info.RequiredStart = splitFields(value)
		case "Required-Stop":
			info.RequiredStop = splitFields(value)
		case "Should-Start":
			info.ShouldStart = splitFields(value)
		case "Should-Stop":
			info.ShouldStop = splitFields(value)
		case "Default-Start":
			info.DefaultStart = splitFields(value)
		case "Default-Stop":
			info.DefaultStop = splitFields(value)
		case "Short-Description":
			info.ShortDescription = value
		case "Description":
			info.Description = value
			descLines = append(descLines, value)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if len(descLines) > 1 {
		info.Description = strings.Join(descLines, " ")
	}

	if !inBlock {
		// No LSB headers found — return empty info (still usable)
		return &LSBInfo{}, nil
	}

	return &info, nil
}

// InitDToServiceDescription converts an init.d script path into a
// ServiceDescription suitable for the slinit loader.
func InitDToServiceDescription(scriptPath string) (*ServiceDescription, error) {
	// Verify the script is executable
	fi, err := os.Stat(scriptPath)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&0111 == 0 {
		return nil, fmt.Errorf("init.d script not executable: %s", scriptPath)
	}

	name := filepath.Base(scriptPath)
	lsb, err := ParseLSBHeaders(scriptPath)
	if err != nil {
		return nil, fmt.Errorf("parsing init.d script '%s': %w", name, err)
	}

	// Source /etc/rc.conf + /etc/conf.d/<name> ahead of each action,
	// matching OpenRC's convention. The wrapper is a no-op when those
	// files are absent, so non-OpenRC distros still work identically
	// to before.
	desc := &ServiceDescription{
		Name:        name,
		Type:        service.TypeScripted,
		Command:     wrapInitdWithConfD(scriptPath, name, "start"),
		StopCommand: wrapInitdWithConfD(scriptPath, name, "stop"),
	}

	if lsb.ShortDescription != "" {
		desc.Description = lsb.ShortDescription
	} else if lsb.Description != "" {
		desc.Description = lsb.Description
	}

	// Map LSB Required-Start → depends-on (hard deps)
	for _, dep := range lsb.RequiredStart {
		mapped := mapFacility(dep)
		if mapped != "" {
			desc.DependsOn = append(desc.DependsOn, mapped)
		}
	}

	// Map LSB Should-Start → waits-for (soft deps)
	for _, dep := range lsb.ShouldStart {
		mapped := mapFacility(dep)
		if mapped != "" {
			desc.WaitsFor = append(desc.WaitsFor, mapped)
		}
	}

	// Provides: first name is the service name, rest are aliases
	if len(lsb.Provides) > 0 {
		desc.Name = lsb.Provides[0]
		if len(lsb.Provides) > 1 {
			// Use first alias as 'provides' (slinit supports one alias)
			desc.Provides = lsb.Provides[1]
		}
	}

	// OpenRC-style depend() extraction. Runs only when we couldn't
	// pull anything useful from LSB headers — Alpine/Void scripts
	// never carry LSB blocks, so this is the actual dep source for
	// those distros. Best-effort: on any error we keep the LSB
	// result (possibly empty) and move on rather than fail the load.
	//
	// The openrc-run shebang also changes how the wrapper invokes
	// the script: /sbin/openrc-run is not shipped by slinit, so an
	// execve of the script would fail with ENOENT on the interpreter
	// line. Instead we source the script and call the start()/stop()
	// shell function directly, mirroring what openrc-run itself does
	// on OpenRC systems.
	if !lsbHasDeps(lsb) {
		if header, err := readShebang(scriptPath); err == nil &&
			LooksLikeOpenRCScript(header) {
			if dep, err := ParseOpenRCDepend(scriptPath); err == nil && dep.HasAny() {
				applyOpenRCDepend(desc, dep)
			}
			desc.Command = wrapOpenRCScript(scriptPath, name, "start")
			desc.StopCommand = wrapOpenRCScript(scriptPath, name, "stop")
		}
	}

	// Don't auto-restart init.d scripts (they're typically one-shot)
	desc.AutoRestart = service.RestartNever

	return desc, nil
}

// lsbHasDeps reports whether the LSB block contributed any dependency
// signal at all. When it did not, we try the OpenRC parser as a
// fallback rather than shipping a service with an empty dep graph.
func lsbHasDeps(lsb *LSBInfo) bool {
	if lsb == nil {
		return false
	}
	return len(lsb.RequiredStart)+len(lsb.ShouldStart)+len(lsb.Provides) > 0
}

// readShebang reads just enough of the script for LooksLikeOpenRCScript
// to inspect the first line. Kept small so we don't slurp arbitrary
// files into memory.
func readShebang(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, 128)
	n, _ := f.Read(buf)
	return string(buf[:n]), nil
}

// applyOpenRCDepend translates a parsed depend() block onto desc.
// Mapping mirrors OpenRC semantics:
//
//	need    → depends-on (hard)
//	use     → waits-for  (soft)
//	want    → waits-for  (modern spelling of use)
//	after   → after      (order-only)
//	before  → before     (order-only)
//	provide → provides   (first only; slinit takes a single alias)
//	keyword → captured but not enforced yet
//
// Virtual facility names ($network etc.) are re-mapped via
// mapFacility so they match slinit's LSBFacilityMap entries.
func applyOpenRCDepend(desc *ServiceDescription, dep *OpenRCDepend) {
	for _, s := range dep.Need {
		if m := mapFacility(s); m != "" {
			desc.DependsOn = append(desc.DependsOn, m)
		}
	}
	for _, s := range dep.Use {
		if m := mapFacility(s); m != "" {
			desc.WaitsFor = append(desc.WaitsFor, m)
		}
	}
	for _, s := range dep.Want {
		if m := mapFacility(s); m != "" {
			desc.WaitsFor = append(desc.WaitsFor, m)
		}
	}
	for _, s := range dep.After {
		if m := mapFacility(s); m != "" {
			desc.After = append(desc.After, m)
		}
	}
	for _, s := range dep.Before {
		if m := mapFacility(s); m != "" {
			desc.Before = append(desc.Before, m)
		}
	}
	if desc.Provides == "" && len(dep.Provide) > 0 {
		// slinit supports a single provides alias; take the first
		// and drop the rest silently. Rare in practice — most scripts
		// list at most one.
		desc.Provides = dep.Provide[0]
	}
}

// mapFacility converts an LSB facility name to a slinit service name.
// Returns "" for $all (ignore) and passes unknown names through.
func mapFacility(name string) string {
	if name == "$all" {
		return "" // skip — too broad
	}
	if mapped, ok := LSBFacilityMap[name]; ok {
		return mapped
	}
	// Not a facility — pass through as service name
	return name
}

// splitFields splits a space-separated string into fields, filtering empty strings.
func splitFields(s string) []string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil
	}
	return fields
}

// IsInitDScript checks if the given path looks like a valid init.d script
// (executable file with a shebang line).
func IsInitDScript(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	if fi.Mode()&0111 == 0 {
		return false
	}

	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 2)
	n, _ := f.Read(buf)
	return n == 2 && string(buf) == "#!"
}
