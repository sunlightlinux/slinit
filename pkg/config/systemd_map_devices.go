package config

import (
	"fmt"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/bpfdev"
	"github.com/sunlightlinux/slinit/pkg/bpfip"
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
			if strings.TrimSpace(trimQuotes(val)) == "" {
				// systemd logs an empty value and keeps the previous.
				return "", []SystemdWarning{{"NOTE", "DevicePolicy= empty: ignored, as systemd ignores it"}}, nil
			}
			p, err := bpfdev.ParsePolicy(trimQuotes(val))
			if err != nil {
				return "", nil, err
			}
			// auto too, so a drop-in relaxing an earlier strict wins.
			return string(p), nil, nil
		}})
	registerService("DeviceAllow", sdRule{custom: mapDeviceAllow})
	registerService("IPAddressAllow", sdRule{custom: mapIPAddressList})
	registerService("IPAddressDeny", sdRule{custom: mapIPAddressList})
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

// mapIPAddressList: IPAddressAllow= / IPAddressDeny= onto
// ip-address-allow / -deny (pkg/bpfip, systemd's bpf-firewall.c
// program); each assignment adds, an empty one clears.
func mapIPAddressList(cfg *SystemdConfig, key, val string) []SystemdWarning {
	slinitKey := "ip-address-allow"
	if key == "IPAddressDeny" {
		slinitKey = "ip-address-deny"
	}
	v := strings.TrimSpace(trimQuotes(val))
	if v == "" {
		cfg.dropMapped(slinitKey)
		return nil
	}
	// systemd skips a word it cannot parse, with a warning. Skipping
	// one of IPAddressAllow= only drops more, so it is skipped here
	// too; one of IPAddressDeny= would let through what the unit
	// meant to keep out, so it refuses the unit.
	var kept []string
	var warns []SystemdWarning
	for _, w := range strings.Fields(v) {
		if _, err := bpfip.ParseList(w); err != nil {
			if key == "IPAddressDeny" {
				cfg.errs = append(cfg.errs, fmt.Sprintf("%s=%s confines the service, and slinit cannot apply it (%v)", key, val, err))
				return nil
			}
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("%s: %v; skipped, as systemd skips it", key, err)})
			continue
		}
		kept = append(kept, w)
	}
	if len(kept) > 0 {
		cfg.mapped = append(cfg.mapped, kvPair{slinitKey, strings.Join(kept, " ")})
	}
	return warns
}

// finishDeviceFilter gives a unit with a device or IP filter a cgroup
// of its own.
func finishDeviceFilter(cfg *SystemdConfig) []SystemdWarning {
	any := false
	for _, k := range []string{"device-allow", "ip-address-allow", "ip-address-deny"} {
		if _, ok := cfg.hasMapped(k); ok {
			any = true
		}
	}
	if v, ok := cfg.hasMapped("device-policy"); ok && v != "auto" {
		any = true
	}
	if !any {
		return nil
	}
	if _, ok := cfg.hasMapped("slice"); ok {
		return nil
	}
	if _, ok := cfg.hasMapped("cgroup"); ok {
		return nil
	}
	cfg.setMapped("slice", "system.slice")
	return []SystemdWarning{{"NOTE", "slice = system.slice: the device and IP filters go on a cgroup of the " +
		"service's own, which systemd gives every service under system.slice"}}
}
