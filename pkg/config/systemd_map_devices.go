package config

import (
	"fmt"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/bpfdev"
)

// DevicePolicy= and DeviceAllow= map onto device-policy and
// device-allow, the same eBPF device filter (pkg/bpfdev builds the
// program systemd's bpf-devices.c builds). Both confine: a value slinit
// cannot take refuses the unit. The filter goes on a cgroup of the
// service's own, which systemd gives every service; a unit with neither
// Slice= nor a cgroup of its own gets slice = system.slice, systemd's
// default for a service, as the loader refuses the filter without one.

func init() {
	registerService("DevicePolicy", sdRule{key: "device-policy", class: sdConfine,
		conv: func(val string) (string, []SystemdWarning, error) {
			p, err := bpfdev.ParsePolicy(trimQuotes(val))
			if err != nil {
				return "", nil, err
			}
			if p == bpfdev.PolicyAuto {
				return "", nil, nil // the default
			}
			return string(p), nil, nil
		}})
	registerService("DeviceAllow", sdRule{custom: mapDeviceAllow})
	registerFinisher(finishDeviceFilter)
}

// mapDeviceAllow: each assignment adds an entry, an empty one clears the
// list; the entry has a space in it, so each is a line of its own.
func mapDeviceAllow(cfg *SystemdConfig, key, val string) []SystemdWarning {
	v := strings.TrimSpace(trimQuotes(val))
	if v == "" {
		cfg.dropMapped("device-allow")
		return nil
	}
	if _, err := bpfdev.ParseAllow(v); err != nil {
		cfg.errs = append(cfg.errs, fmt.Sprintf("%s=%s confines the service, and slinit cannot apply it (%v)", key, val, err))
		return nil
	}
	cfg.mapped = append(cfg.mapped, kvPair{"device-allow", v})
	return nil
}

func finishDeviceFilter(cfg *SystemdConfig) []SystemdWarning {
	_, policy := cfg.hasMapped("device-policy")
	_, allow := cfg.hasMapped("device-allow")
	if !policy && !allow {
		return nil
	}
	if _, ok := cfg.hasMapped("slice"); ok {
		return nil
	}
	if _, ok := cfg.hasMapped("cgroup"); ok {
		return nil
	}
	cfg.setMapped("slice", "system.slice")
	return []SystemdWarning{{"NOTE", "slice = system.slice: the device filter goes on a cgroup of the " +
		"service's own, which systemd gives every service under system.slice"}}
}
