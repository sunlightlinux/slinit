package config

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Resource control: systemd.resource-control(5) and the scheduling,
// rlimit and memory-policy part of systemd.exec(5).
//
// Most of these end up as a cgroup v2 file write, which is how systemd
// implements them too, so the translation is to the value systemd itself
// would write (cgroup.c), not to the value the unit spells. Several
// directives share one cgroup file or one slinit setting — CPUQuota= and
// CPUQuotaPeriodSec= make one cpu.max line, MemoryLimit= yields to any
// MemoryMax=-family setting wherever it appears — so those keep the
// systemd state of the unit in sdResState and re-render their slinit
// lines after every assignment. The outcome then does not depend on the
// order the directives appear in, as it does not in systemd.
//
// Three things systemd does that slinit does not, and that decide most
// of the gaps below:
//
//   - systemd resolves a device path (IOReadBandwidthMax=/dev/sda 1M) to
//     its MAJ:MIN when it applies the setting; slinit's cgroup-setting
//     writes a literal line. A MAJ:MIN fixed when the unit is converted
//     names whatever disk holds that number after the next reboot, so
//     the per-device io settings are not translated.
//   - "Startup" values in systemd hold while the system boots and shuts
//     down; slinit only has startup cpusets, which hold while the service
//     itself starts.
//   - the eBPF and netfilter programs systemd attaches to a unit's cgroup
//     (device, IP, socket-bind, interface filters) have no slinit
//     counterpart.
//
// The cgroup settings only reach a cgroup of the service's own when the
// slinit service has slice= or cgroup=; see cgroupNote.

func init() {
	for _, k := range []string{"MemoryMin", "MemoryLow", "MemoryHigh", "MemoryMax",
		"MemorySwapMax", "MemoryZSwapMax", "MemoryLimit"} {
		registerService(k, sdRule{custom: resMemory})
	}
	registerService("TasksMax", sdRule{custom: resTasksMax})
	registerService("CPUWeight", sdRule{custom: resCPUWeight})
	registerService("CPUShares", sdRule{custom: resCPUWeight})
	registerService("CPUQuota", sdRule{custom: resCPUQuota})
	registerService("CPUQuotaPeriodSec", sdRule{custom: resCPUQuota})
	registerService("IOWeight", sdRule{custom: resIOWeight})
	registerService("BlockIOWeight", sdRule{custom: resIOWeight})
	registerService("IOSchedulingClass", sdRule{custom: resIOPrio})
	registerService("IOSchedulingPriority", sdRule{custom: resIOPrio})
	registerService("CPUSchedulingPolicy", sdRule{custom: resSched})
	registerService("CPUSchedulingPriority", sdRule{custom: resSched})
	registerService("NUMAPolicy", sdRule{custom: resNUMA})
	registerService("NUMAMask", sdRule{custom: resNUMA})
	for _, k := range []string{"CPUAffinity", "AllowedCPUs", "AllowedMemoryNodes",
		"StartupAllowedCPUs", "StartupAllowedMemoryNodes"} {
		registerService(k, sdRule{custom: resCPUSet})
	}
	registerService("CoredumpFilter", sdRule{custom: resCoredumpFilter})
	registerService("MemoryZSwapWriteback", sdRule{custom: resZSwapWriteback})

	registerService("CPUSchedulingResetOnFork", resScalar("sched-reset-on-fork", false, convBool))
	registerService("MemoryKSM", resScalar("memory-ksm", false, convTristate))
	registerService("MemoryTHP", resScalar("memory-thp", false, convMemoryTHP))
	registerService("CPUSetPartition", resScalar("cpuset-partition", true, convCPUSetPartition))
	for sd, key := range map[string]string{
		"CacheDirectoryQuota": "cache-directory-quota",
		"LogsDirectoryQuota":  "logs-directory-quota",
		"StateDirectoryQuota": "state-directory-quota",
	} {
		registerService(sd, resScalar(key, false, convDirectoryQuota))
	}
	for sd, key := range map[string]string{
		"CacheDirectoryAccounting": "cache-directory-accounting",
		"LogsDirectoryAccounting":  "logs-directory-accounting",
		"StateDirectoryAccounting": "state-directory-accounting",
	} {
		registerService(sd, resScalar(key, false, convBool))
	}

	gap := func(confine bool, why string, keys ...string) {
		for _, k := range keys {
			sdServiceNoEquivalent[k] = sdGap{confine: confine, why: why}
		}
	}

	// Per-device io settings: see the package comment above.
	gap(false, "slinit writes io.weight/io.max/io.latency lines literally and so needs the device's MAJ:MIN, "+
		"which systemd resolves from the path when it starts the service; add `cgroup-setting = io.max MAJ:MIN rbps=N` "+
		"(or io.weight / io.latency) by hand for the machine it runs on",
		"IOReadBandwidthMax", "IOWriteBandwidthMax", "IOReadIOPSMax", "IOWriteIOPSMax",
		"IODeviceWeight", "IODeviceLatencyTargetSec")
	gap(false, "removed in systemd 258; systemd 257 and earlier applied it as IODeviceWeight=/IO*BandwidthMax=, "+
		"which slinit cannot translate either (it needs the device's MAJ:MIN): add `cgroup-setting = io.weight|io.max MAJ:MIN ...` by hand",
		"BlockIODeviceWeight", "BlockIOReadBandwidth", "BlockIOWriteBandwidth")

	// Boot-time values: see the package comment above.
	gap(false, "slinit has no value that holds only while the system boots; the service gets the non-Startup setting from the start",
		"StartupCPUWeight", "StartupIOWeight", "StartupMemoryHigh", "StartupMemoryLow", "StartupMemoryMax",
		"StartupMemorySwapMax", "StartupMemoryZSwapMax")
	gap(false, "removed in systemd 258, and slinit has no value that holds only while the system boots; "+
		"the service gets CPUShares=/BlockIOWeight= (or their successors) from the start",
		"StartupCPUShares", "StartupBlockIOWeight")

	// systemd's pressure protocol is for the service, not for systemd:
	// it exports $MEMORY_PRESSURE_WATCH (the cgroup's pressure file) and
	// $MEMORY_PRESSURE_WRITE (the trigger to arm there) so the service
	// watches its own pressure, as sd_event_add_memory_pressure() does.
	// slinit's *-pressure-watch arms the trigger itself and emits a
	// service event for monitors, and sets no variable — a different
	// mechanism with a different consumer, so it is not mapped.
	gap(false, "systemd passes the service $*_PRESSURE_WATCH/$*_PRESSURE_WRITE so it watches its own pressure; "+
		"slinit sets neither (its *-pressure-watch makes slinit emit a pressure event instead), so the service falls back to its own default",
		"MemoryPressureWatch", "MemoryPressureThresholdSec", "CPUPressureWatch", "CPUPressureThresholdSec",
		"IOPressureWatch", "IOPressureThresholdSec")

	// rlimits slinit does not set. The service inherits slinit's own.
	gap(false, "slinit sets only rlimit-nofile, -core, -data, -as and -memlock; the service inherits slinit's own limit",
		"LimitCPU", "LimitFSIZE", "LimitLOCKS", "LimitMSGQUEUE", "LimitNICE", "LimitRSS",
		"LimitRTPRIO", "LimitRTTIME", "LimitSIGPENDING", "LimitSTACK")
	// LimitNPROC= is the one units use as a sandbox (LimitNPROC=1: the
	// service cannot fork), so losing it is a confinement gap.
	gap(true, "slinit sets only rlimit-nofile, -core, -data, -as and -memlock; the service can fork as much as "+
		"slinit's own RLIMIT_NPROC allows (cgroup-pids-max caps the service's tasks, not the user's)",
		"LimitNPROC")

	// eBPF and netfilter based confinement.
	gap(true, "slinit has no device cgroup filter (systemd attaches an eBPF device program); "+
		"use private-devices or a mount namespace to hide device nodes",
		"DeviceAllow", "DevicePolicy")
	gap(true, "slinit has no per-service IP filter (systemd attaches an eBPF program to the cgroup); "+
		"use namespace-net or a host firewall rule matching the service's cgroup",
		"IPAddressAllow", "IPAddressDeny", "IPIngressFilterPath", "IPEgressFilterPath")
	gap(true, "slinit has no socket-bind filter (systemd attaches an eBPF program to the cgroup)",
		"SocketBindAllow", "SocketBindDeny")
	gap(true, "slinit has no per-service network interface filter (systemd attaches an eBPF program to the cgroup); "+
		"use namespace-net",
		"RestrictNetworkInterfaces")
	gap(true, "slinit attaches no BPF program to a service's cgroup", "BPFProgram")
	gap(true, "slinit adds no cgroup, user or group ID to NFT sets, so firewall rules that select the service through the set do not match it",
		"NFTSet")
	gap(false, "slinit mounts no private bpffs and so delegates no BPF token rights",
		"BPFDelegateCommands", "BPFDelegateMaps", "BPFDelegatePrograms", "BPFDelegateAttachments")

	// Delegation details.
	gap(false, "slinit starts the payload in the delegated cgroup itself; the payload must move into a child cgroup "+
		"before it enables controllers there",
		"DelegateSubgroup")
	gap(false, "slinit does not hand namespace ownership to a user namespace of the service",
		"DelegateNamespaces")
	gap(false, "slinit does not withhold controllers from a service's cgroup subtree; "+
		"`delegate = <controllers>` names the ones handed over",
		"DisableControllers")

	// systemd-oomd.
	gap(false, "slinit has no userspace OOM killer acting on pressure or swap use; the kernel OOM killer and "+
		"oom-policy remain, and memory-high throttles before memory-max kills",
		"ManagedOOMSwap", "ManagedOOMMemoryPressure", "ManagedOOMMemoryPressureLimit",
		"ManagedOOMMemoryPressureDurationSec", "ManagedOOMPreference", "OOMRules")

	// Accounting only feeds systemd's own reporting. slinit enables a
	// controller in the parent when a setting needs it (attrs.go,
	// enableSubtreeControllers), which is all that accounting changes
	// for the limits.
	for _, k := range []string{"MemoryAccounting", "IOAccounting", "TasksAccounting", "IPAccounting"} {
		sdServiceIgnored[k] = "only switches systemd's own usage reporting (systemctl status, systemd-cgtop)"
	}
	sdServiceIgnored["CPUAccounting"] = "removed in systemd 258, which always accounts CPU; it only ever switched systemd's own reporting"
	sdServiceIgnored["BlockIOAccounting"] = "removed in systemd 258; it only ever switched systemd's own reporting"
	sdServiceIgnored["NetClass"] = "removed from systemd long ago (the net_cls controller is cgroup v1); systemd ignores it"
	for _, k := range []string{"DefaultMemoryLow", "DefaultMemoryMin", "DefaultStartupMemoryLow"} {
		sdServiceIgnored[k] = "removed in systemd 260, which ignores it; it only set defaults for the unit's child cgroups"
	}
}

// sdResState is the systemd state of the directives whose slinit lines
// depend on more than one of them.
type sdResState struct {
	mem map[string]uint64 // MemoryMin= … MemoryLimit=, in bytes or sdLimitMax

	cpuWeight, cpuShares   uint64 // 0: unset; cpuWeightIdle for CPUWeight=idle
	quotaPerSec, period    uint64 // usec; 0: unset
	ioWeight, blkioWeight  uint64 // 0: unset
	ioClass, ioLevel       int
	ioSet                  bool
	schedPolicy, schedPrio int
	schedSet               bool
	numaPolicy             string // "": unset
	numaMask               cpuSet
	affinity               cpuSet
	affinityFromNUMA       bool
	cpus, mems             cpuSet
	startupCPUs            cpuSet
	startupMems            cpuSet
	coredumpMask           uint64
	coredumpSet            bool
	cgroupNoted            bool
}

const (
	sdLimitMax    = math.MaxUint64 // systemd's CGROUP_LIMIT_MAX: "infinity"
	cpuWeightIdle = math.MaxUint64
)

// resState returns the unit's resource-control state, made on first use.
func resState(cfg *SystemdConfig) *sdResState {
	if cfg.res == nil {
		cfg.res = &sdResState{mem: map[string]uint64{}}
	}
	return cfg.res
}

// resScalar is a one-to-one mapping whose empty conv result means "the
// slinit default": unlike a plain rule it then also drops an earlier
// assignment, since in systemd an empty or default value resets the
// directive. cgroup marks a setting that needs the service's own cgroup.
func resScalar(key string, cgroup bool, conv func(string) (string, []SystemdWarning, error)) sdRule {
	return sdRule{custom: func(cfg *SystemdConfig, sd, val string) []SystemdWarning {
		out, warns, err := conv(strings.TrimSpace(trimQuotes(val)))
		if err != nil {
			return append(warns, resFailed(cfg, sd, val, err)...)
		}
		if out == "" {
			cfg.dropMapped(key)
			return warns
		}
		if err := validateSlinitSetting(key, out); err != nil {
			return append(warns, resFailed(cfg, sd, val, fmt.Errorf("%s = %s: %w", key, out, err))...)
		}
		cfg.setMapped(key, out)
		if cgroup {
			warns = append(warns, cgroupNote(cfg, resState(cfg))...)
		}
		return warns
	}}
}

// resFailed drops a value slinit cannot take. Every directive here is
// behaviour, not confinement: systemd itself logs an invalid value and
// carries on without it.
func resFailed(cfg *SystemdConfig, sd, val string, err error) []SystemdWarning {
	return cfg.ruleFailed("Service", sd, val, sdRule{class: sdBehaviour}, err)
}

// cgroupNote warns, once per unit, that the cgroup settings only reach
// a cgroup of the service's own when it has slice= or cgroup=. systemd
// gives every service one (system.slice/<name>.service); a slinit
// service without either is started in the daemon's --cgroup-path,
// shared by every such service, where a memory.max meant for one caps
// them all — or, with no --cgroup-path, nowhere, and the limit is lost.
// A Slice= later in the unit makes this note moot; it cannot be taken
// back, so it says so.
func cgroupNote(cfg *SystemdConfig, st *sdResState) []SystemdWarning {
	// Only recorded here: whether the unit gives the service a cgroup of
	// its own is known once the whole unit (Slice= may come later, or in
	// a drop-in) has been read. finishSystemdUnit reports it.
	st.cgroupNoted = true
	return nil
}

// cgroupWarning is the one warning for a unit that maps cgroup settings
// without a cgroup of the service's own (slice= or cgroup=).
func cgroupWarning(cfg *SystemdConfig) []SystemdWarning {
	if cfg.res == nil || !cfg.res.cgroupNoted {
		return nil
	}
	for _, kv := range cfg.mapped {
		if kv.key == "slice" || kv.key == "cgroup" {
			return nil
		}
	}
	return []SystemdWarning{{"WARN", "cgroup resource settings apply to a cgroup of the service's own only " +
		"with slice= or cgroup= (systemd gives every service one); add `slice = system.slice` — " +
		"otherwise slinit writes them to the daemon's --cgroup-path, shared by every service without its own, or nowhere"}}
}

// setCgroupFile sets the cgroup-setting line for one cgroup file,
// replacing an earlier one for the same file and keeping the lines for
// other files: several cgroup-setting lines are valid, one per file.
func setCgroupFile(cfg *SystemdConfig, file, value string) {
	for i, kv := range cfg.mapped {
		if kv.key == "cgroup-setting" && strings.HasPrefix(kv.value, file+" ") {
			cfg.mapped[i].value = file + " " + value
			return
		}
	}
	cfg.mapped = append(cfg.mapped, kvPair{"cgroup-setting", file + " " + value})
}

func dropCgroupFile(cfg *SystemdConfig, file string) {
	kept := cfg.mapped[:0]
	for _, kv := range cfg.mapped {
		if !(kv.key == "cgroup-setting" && strings.HasPrefix(kv.value, file+" ")) {
			kept = append(kept, kv)
		}
	}
	cfg.mapped = kept
}

// setOrDrop sets key to value, or drops it when value is empty.
func setOrDrop(cfg *SystemdConfig, key, value string) {
	if value == "" {
		cfg.dropMapped(key)
	} else {
		cfg.setMapped(key, value)
	}
}

// --- value grammars, as systemd parses them ---

// sdParseBool is systemd's parse_boolean.
func sdParseBool(v string) (bool, error) {
	switch strings.ToLower(v) {
	case "1", "yes", "y", "true", "t", "on":
		return true, nil
	case "0", "no", "n", "false", "f", "off":
		return false, nil
	}
	return false, fmt.Errorf("%q is not a boolean", v)
}

// convBool maps a systemd boolean (config_parse_bool, which takes no
// empty value) onto a slinit yes/no setting whose default is no, so no
// emits nothing.
func convBool(v string) (string, []SystemdWarning, error) {
	b, err := sdParseBool(v)
	if err != nil || !b {
		return "", nil, err
	}
	return "yes", nil, nil
}

// convTristate is convBool for config_parse_tristate, where empty is
// "unset".
func convTristate(v string) (string, []SystemdWarning, error) {
	if v == "" {
		return "", nil, nil
	}
	return convBool(v)
}

// sdPartsInt is percent-util.c's parse_parts_integer: plain decimal,
// no sign, no leading zero.
func sdPartsInt(s string) (int, bool) {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 31)
	if err != nil {
		return 0, false
	}
	return int(n), true
}

// sdPermyriad is parse_permyriad(_unbounded): "50%", "12.5%", "12.34%",
// "5‰", "5.5‰", "5‱", in ten-thousandths. ok is false when v carries
// none of the three signs, which in systemd means "try a size instead";
// with a sign, a malformed or (bounded) over-100% value is an error.
func sdPermyriad(v string, bounded bool) (r int, ok bool, err error) {
	var whole, frac string
	var scale, places int
	switch {
	case strings.HasSuffix(v, "‱"):
		whole, scale, places = strings.TrimSuffix(v, "‱"), 1, 0
	case strings.HasSuffix(v, "‰"):
		whole, scale, places = strings.TrimSuffix(v, "‰"), 10, 1
	case strings.HasSuffix(v, "%"):
		whole, scale, places = strings.TrimSuffix(v, "%"), 100, 2
	default:
		return 0, false, nil
	}
	if i := strings.IndexByte(whole, '.'); i >= 0 {
		whole, frac = whole[:i], whole[i+1:]
		if places == 0 || len(frac) == 0 || len(frac) > places {
			return 0, true, fmt.Errorf("%q is not a percentage", v)
		}
	}
	n, good := sdPartsInt(whole)
	if !good {
		return 0, true, fmt.Errorf("%q is not a percentage", v)
	}
	q := 0
	for i := 0; i < places; i++ {
		q *= 10
		if i < len(frac) {
			if frac[i] < '0' || frac[i] > '9' {
				return 0, true, fmt.Errorf("%q is not a percentage", v)
			}
			q += int(frac[i] - '0')
		}
	}
	if n > (math.MaxInt32-q)/scale {
		return 0, true, fmt.Errorf("%q is out of range", v)
	}
	r = n*scale + q
	if bounded && r > 10000 {
		return 0, true, fmt.Errorf("%q is over 100%%", v)
	}
	return r, true, nil
}

// sdSizeSuffixes is parse_size's base-1024 table, in its order: each
// part of a sum must use a smaller suffix than the one before it.
var sdSizeSuffixes = []struct {
	suffix string
	factor uint64
}{
	{"E", 1 << 60}, {"P", 1 << 50}, {"T", 1 << 40}, {"G", 1 << 30},
	{"M", 1 << 20}, {"K", 1 << 10}, {"B", 1}, {"", 1},
}

// sdParseSize is systemd's parse_size(t, 1024): "512M", "1.5G", a sum
// "1G 512M", or plain bytes. Suffixes are upper case only, as there.
func sdParseSize(t string) (uint64, error) {
	bad := fmt.Errorf("%q is not a size", t)
	var total uint64
	start := 0
	p := t
	for {
		p = strings.TrimLeft(p, " \t\n")
		i := 0
		for i < len(p) && p[i] >= '0' && p[i] <= '9' {
			i++
		}
		if i == 0 {
			return 0, bad
		}
		l, err := strconv.ParseUint(p[:i], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%q is out of range", t)
		}
		e := p[i:]
		frac := 0.0
		if strings.HasPrefix(e, ".") {
			e = e[1:]
			j := 0
			for j < len(e) && e[j] >= '0' && e[j] <= '9' {
				j++
			}
			if j > 0 {
				// The same float steps as parse_size, so a fraction
				// truncates to the same byte.
				if l2, err := strconv.ParseUint(e[:j], 10, 64); err == nil {
					frac = float64(l2)
					for k := 0; k < j; k++ {
						frac /= 10
					}
				}
				e = e[j:]
			}
		}
		e = strings.TrimLeft(e, " \t\n")
		k := start
		for k < len(sdSizeSuffixes) && !strings.HasPrefix(e, sdSizeSuffixes[k].suffix) {
			k++
		}
		if k >= len(sdSizeSuffixes) {
			return 0, bad
		}
		f := sdSizeSuffixes[k].factor
		bump := uint64(0)
		if frac > 0 {
			bump = 1
		}
		if l+bump > math.MaxUint64/f {
			return 0, fmt.Errorf("%q is out of range", t)
		}
		part := l*f + uint64(frac*float64(f))
		if part > math.MaxUint64-total {
			return 0, fmt.Errorf("%q is out of range", t)
		}
		total += part
		p = e[len(sdSizeSuffixes[k].suffix):]
		start = k + 1
		if p == "" {
			return total, nil
		}
	}
}

// sdTimespanUsec parses a systemd time span into microseconds.
func sdTimespanUsec(v string) (uint64, bool) {
	s, ok := parseSystemdSeconds(v, "")
	if !ok {
		return 0, false
	}
	whole, frac, _ := strings.Cut(s, ".")
	w, err := strconv.ParseUint(whole, 10, 64)
	if err != nil || w > math.MaxUint64/1000000 {
		return 0, false
	}
	frac = (frac + "000000")[:6]
	f, _ := strconv.ParseUint(frac, 10, 64)
	return w*1e6 + f, true
}

// sdPhysicalMemory is systemd's physical_memory(): RAM, capped by the
// root cgroup's memory.max, which exists only inside a cgroup namespace
// (a container with a memory limit). A variable so tests can fix it.
var sdPhysicalMemory = func() (uint64, error) {
	var si unix.Sysinfo_t
	if err := unix.Sysinfo(&si); err != nil {
		return 0, err
	}
	mem := uint64(si.Totalram) * uint64(si.Unit)
	if lim, ok := readUintFile("/sys/fs/cgroup/memory.max"); ok {
		ps := uint64(os.Getpagesize())
		if lim = lim / ps * ps; lim < mem {
			mem = lim
		}
	}
	return mem, nil
}

// sdSystemTasksMax is systemd's system_tasks_max(): the least of
// kernel.threads-max, kernel.pid_max - 1 and the root cgroup's
// pids.max, starting from systemd's TASKS_MAX.
var sdSystemTasksMax = func() (uint64, error) {
	t := uint64(4194303)
	if v, ok := readUintFile("/proc/sys/kernel/threads-max"); ok && v < t {
		t = v
	}
	if v, ok := readUintFile("/proc/sys/kernel/pid_max"); ok && v > 0 && v-1 < t {
		t = v - 1
	}
	if v, ok := readUintFile("/sys/fs/cgroup/pids.max"); ok && v < t {
		t = v
	}
	return t, nil
}

// sdNUMAMaxNode is numa_max_node(): the highest nodeN directory under
// /sys/devices/system/node, for NUMAMask=all.
var sdNUMAMaxNode = func() (int, error) {
	ents, err := os.ReadDir("/sys/devices/system/node")
	if err != nil {
		return 0, err
	}
	maxNode := 0
	for _, e := range ents {
		if n, ok := strings.CutPrefix(e.Name(), "node"); ok && e.IsDir() {
			if i, err := strconv.Atoi(n); err == nil && i > maxNode {
				maxNode = i
			}
		}
	}
	return maxNode, nil
}

func readUintFile(path string) (uint64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	return v, err == nil
}

// --- memory ---

// memDefault is each memory directive's unset value: 0 for the
// protections, no limit for the rest.
func memDefault(sd string) uint64 {
	if sd == "MemoryMin" || sd == "MemoryLow" {
		return 0
	}
	return sdLimitMax
}

func (st *sdResState) memValue(sd string) uint64 {
	if v, ok := st.mem[sd]; ok {
		return v
	}
	return memDefault(sd)
}

// resMemory parses a memory directive as config_parse_memory_limit does
// and re-renders the memory lines.
func resMemory(cfg *SystemdConfig, sd, val string) []SystemdWarning {
	st := resState(cfg)
	v := strings.TrimSpace(trimQuotes(val))
	var warns []SystemdWarning
	// Empty is the default; infinity is no limit, also for the
	// protections (MemoryLow=infinity protects everything).
	bytes := memDefault(sd)
	if v == "infinity" {
		bytes = sdLimitMax
	} else if v != "" {
		r, isPct, err := sdPermyriad(v, true)
		switch {
		case err != nil:
			return resFailed(cfg, sd, val, err)
		case isPct:
			mem, err := sdPhysicalMemory()
			if err != nil {
				return resFailed(cfg, sd, val, fmt.Errorf("cannot read this machine's RAM: %w", err))
			}
			bytes = memScale(mem, uint64(r))
			warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf(
				"%s=%s is %d bytes of this machine's memory, computed now, as systemd computes it when it loads the unit; "+
					"a converted file moved to another machine keeps that number", sd, v, bytes)})
		default:
			if bytes, err = sdParseSize(v); err != nil {
				return resFailed(cfg, sd, val, err)
			}
		}
		zeroOK := sd == "MemorySwapMax" || sd == "MemoryZSwapMax" || sd == "MemoryLow" || sd == "MemoryMin"
		if bytes == sdLimitMax || (bytes == 0 && !zeroOK) {
			return resFailed(cfg, sd, val, fmt.Errorf("memory limit out of range"))
		}
	}
	st.mem[sd] = bytes
	if sd == "MemoryLimit" {
		warns = append(warns, SystemdWarning{"NOTE", "MemoryLimit= was removed in systemd 258, which ignores it; " +
			"it is applied as systemd 257 and earlier did, as MemoryMax= when no MemoryMin/Low/High/Max/SwapMax/ZSwapMax= is set"})
	}
	return append(warns, renderMemory(cfg, st)...)
}

// memScale is physical_memory_scale(r, 10000): whole pages, rounded down.
func memScale(mem, r uint64) uint64 {
	if r == 0 {
		return 0
	}
	if r == 10000 {
		return mem
	}
	ps := uint64(os.Getpagesize())
	p := mem / ps
	if r > math.MaxUint64/p {
		return sdLimitMax
	}
	return p * r / 10000 * ps
}

// renderMemory writes what cgroup.c writes: each knob that is not at its
// default, and memory.max from MemoryLimit= only when none of the
// unified settings is given (systemd 257's unit_has_unified_memory_config).
func renderMemory(cfg *SystemdConfig, st *sdResState) []SystemdWarning {
	fmtMem := func(v uint64) string {
		if v == sdLimitMax {
			return "max"
		}
		return strconv.FormatUint(v, 10)
	}
	unified := false
	line := func(sd string) string {
		v := st.memValue(sd)
		if v == memDefault(sd) {
			return ""
		}
		unified = true
		return fmtMem(v)
	}
	minV, lowV, highV := line("MemoryMin"), line("MemoryLow"), line("MemoryHigh")
	maxV, swapV, zswapV := line("MemoryMax"), line("MemorySwapMax"), line("MemoryZSwapMax")
	if !unified {
		if l := st.memValue("MemoryLimit"); l != sdLimitMax {
			maxV = fmtMem(l)
		}
	}
	setOrDrop(cfg, "cgroup-memory-min", minV)
	setOrDrop(cfg, "cgroup-memory-low", lowV)
	setOrDrop(cfg, "cgroup-memory-high", highV)
	setOrDrop(cfg, "cgroup-memory-max", maxV)
	setOrDrop(cfg, "cgroup-swap-max", swapV)
	if zswapV == "" {
		dropCgroupFile(cfg, "memory.zswap.max")
	} else {
		setCgroupFile(cfg, "memory.zswap.max", zswapV)
	}
	if minV+lowV+highV+maxV+swapV+zswapV != "" {
		return cgroupNote(cfg, st)
	}
	return nil
}

// resZSwapWriteback: memory.zswap.writeback is 1 unless turned off, so
// only "no" needs a line.
func resZSwapWriteback(cfg *SystemdConfig, sd, val string) []SystemdWarning {
	b, err := sdParseBool(strings.TrimSpace(trimQuotes(val)))
	if err != nil {
		return resFailed(cfg, sd, val, err)
	}
	if b {
		dropCgroupFile(cfg, "memory.zswap.writeback")
		return nil
	}
	setCgroupFile(cfg, "memory.zswap.writeback", "0")
	return cgroupNote(cfg, resState(cfg))
}

// --- tasks ---

// resTasksMax: pids.max. systemd resolves a percentage against
// system_tasks_max() when it applies the setting; this resolves it when
// the unit is loaded, so a kernel.pid_max raised later (systemd's own
// sysctl.d raises it at boot) is not seen.
func resTasksMax(cfg *SystemdConfig, sd, val string) []SystemdWarning {
	v := strings.TrimSpace(trimQuotes(val))
	if v == "" || v == "infinity" {
		// Empty is systemd's DefaultTasksMax= (15% of the system's
		// tasks); slinit has no such default and sets no limit.
		cfg.dropMapped("cgroup-pids-max")
		return nil
	}
	var warns []SystemdWarning
	var n uint64
	r, isPct, err := sdPermyriad(v, true)
	switch {
	case err != nil:
		return resFailed(cfg, sd, val, err)
	case isPct:
		t, err := sdSystemTasksMax()
		if err != nil {
			return resFailed(cfg, sd, val, err)
		}
		n = t * uint64(r) / 10000
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf(
			"TasksMax=%s is %d tasks on this machine now; systemd computes it from kernel.pid_max and threads-max "+
				"when the service starts, so a limit raised after this unit is loaded is not seen", v, n)})
	default:
		if n, err = strconv.ParseUint(v, 10, 64); err != nil || n == 0 || n == math.MaxUint64 {
			return resFailed(cfg, sd, val, fmt.Errorf("not a task count"))
		}
	}
	cfg.setMapped("cgroup-pids-max", strconv.FormatUint(n, 10))
	return append(warns, cgroupNote(cfg, resState(cfg))...)
}

// --- CPU ---

// sdWeight is cg_weight_parse: 1..10000.
func sdWeight(v string, lo, hi uint64) (uint64, error) {
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("weight %q out of range %d..%d", v, lo, hi)
	}
	return n, nil
}

// resCPUWeight: CPUWeight=, and the CPUShares= systemd 258 removed. 257
// applied CPUShares= only without a CPUWeight=, as
// cgroup_cpu_shares_to_weight(shares) = shares*100/1024 in 1..10000.
// CPUWeight=idle is cpu.idle = 1, with cpu.weight left alone.
func resCPUWeight(cfg *SystemdConfig, sd, val string) []SystemdWarning {
	st := resState(cfg)
	v := strings.TrimSpace(trimQuotes(val))
	var warns []SystemdWarning
	switch {
	case sd == "CPUShares":
		w := uint64(0)
		if v != "" {
			s, err := sdWeight(v, 2, 262144)
			if err != nil {
				return resFailed(cfg, sd, val, err)
			}
			w = min(max(s*100/1024, 1), 10000)
		}
		st.cpuShares = w
		warns = append(warns, SystemdWarning{"NOTE", "CPUShares= was removed in systemd 258, which ignores it; " +
			"it is applied as systemd 257 and earlier did, as CPUWeight=shares*100/1024 when no CPUWeight= is set"})
	case v == "":
		st.cpuWeight = 0
	case v == "idle":
		st.cpuWeight = cpuWeightIdle
	default:
		w, err := sdWeight(v, 1, 10000)
		if err != nil {
			return resFailed(cfg, sd, val, err)
		}
		st.cpuWeight = w
	}
	w := st.cpuWeight
	if w == 0 {
		w = st.cpuShares
	}
	switch w {
	case 0:
		cfg.dropMapped("cgroup-cpu-weight")
		dropCgroupFile(cfg, "cpu.idle")
		return warns
	case cpuWeightIdle:
		cfg.dropMapped("cgroup-cpu-weight")
		setCgroupFile(cfg, "cpu.idle", "1")
	default:
		dropCgroupFile(cfg, "cpu.idle")
		cfg.setMapped("cgroup-cpu-weight", strconv.FormatUint(w, 10))
	}
	return append(warns, cgroupNote(cfg, st)...)
}

// resCPUQuota: CPUQuota= and CPUQuotaPeriodSec= make one cpu.max line,
// computed as cgroup_apply_cpu_quota does: the period (100ms unless set)
// is clamped so that it lies in 1ms..1s and the quota for it is at least
// 1ms, and the quota written is max(quota_per_sec * period / 1s, 1ms).
// Without a quota systemd writes "max 100000", the kernel default.
func resCPUQuota(cfg *SystemdConfig, sd, val string) []SystemdWarning {
	st := resState(cfg)
	v := strings.TrimSpace(trimQuotes(val))
	if sd == "CPUQuota" {
		if v == "" {
			st.quotaPerSec = 0
		} else {
			r, isPct, err := sdPermyriad(v, false)
			if err == nil && (!isPct || r <= 0) {
				err = fmt.Errorf("%q is not a positive percentage", v)
			}
			if err != nil {
				return resFailed(cfg, sd, val, err)
			}
			st.quotaPerSec = uint64(r) * 1e6 / 10000
		}
	} else {
		switch v {
		case "", "infinity":
			st.period = 0
		default:
			us, ok := sdTimespanUsec(v)
			if !ok {
				return resFailed(cfg, sd, val, fmt.Errorf("%q is no time span", v))
			}
			// 0 is clamped to 1ms below, as systemd clamps it.
			st.period = max(us, 1)
		}
	}
	if st.quotaPerSec == 0 {
		cfg.dropMapped("cgroup-cpu-max")
		return nil
	}
	const msec, sec = 1000, 1000000
	period := uint64(100000)
	if st.period != 0 {
		period = st.period
	}
	adj := min(max(period, msec, msec*sec/st.quotaPerSec), sec)
	var warns []SystemdWarning
	if adj != period {
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf(
			"CPU quota period clamped to %dus, as systemd clamps it (1ms..1s, and at least 1ms of quota per period)", adj)})
	}
	quota := max(st.quotaPerSec*adj/sec, msec)
	cfg.setMapped("cgroup-cpu-max", fmt.Sprintf("%d %d", quota, adj))
	return append(warns, cgroupNote(cfg, st)...)
}

// --- IO ---

// resIOWeight: IOWeight=, and the BlockIOWeight= systemd 258 removed.
// 257 applied BlockIOWeight= only without an io setting, as
// cgroup_weight_blkio_to_io(w) = w*100/500 in 1..10000. slinit writes
// io.weight only; systemd also writes io.bfq.weight, which is the file
// the BFQ scheduler reads.
func resIOWeight(cfg *SystemdConfig, sd, val string) []SystemdWarning {
	st := resState(cfg)
	v := strings.TrimSpace(trimQuotes(val))
	var warns []SystemdWarning
	if sd == "BlockIOWeight" {
		w := uint64(0)
		if v != "" {
			b, err := sdWeight(v, 10, 1000)
			if err != nil {
				return resFailed(cfg, sd, val, err)
			}
			w = min(max(b*100/500, 1), 10000)
		}
		st.blkioWeight = w
		warns = append(warns, SystemdWarning{"NOTE", "BlockIOWeight= was removed in systemd 258, which ignores it; " +
			"it is applied as systemd 257 and earlier did, as IOWeight=weight/5 when no IOWeight= is set"})
	} else {
		w := uint64(0)
		if v != "" {
			var err error
			if w, err = sdWeight(v, 1, 10000); err != nil {
				return resFailed(cfg, sd, val, err)
			}
		}
		st.ioWeight = w
	}
	w := st.ioWeight
	if w == 0 {
		w = st.blkioWeight
	}
	if w == 0 {
		cfg.dropMapped("cgroup-io-weight")
		return warns
	}
	cfg.setMapped("cgroup-io-weight", strconv.FormatUint(w, 10))
	return append(warns, cgroupNote(cfg, st)...)
}

// resIOPrio: IOSchedulingClass= and IOSchedulingPriority= are one I/O
// priority, starting from systemd's default best-effort/4; class "none"
// normalises to that default (ioprio_normalize). Either one empty resets
// both.
func resIOPrio(cfg *SystemdConfig, sd, val string) []SystemdWarning {
	st := resState(cfg)
	v := strings.TrimSpace(trimQuotes(val))
	if !st.ioSet {
		st.ioClass, st.ioLevel = 2, 4
	}
	switch {
	case v == "":
		st.ioSet = false
		cfg.dropMapped("ioprio")
		return nil
	case sd == "IOSchedulingClass":
		classes := map[string]int{"none": 0, "realtime": 1, "best-effort": 2, "idle": 3, "0": 0, "1": 1, "2": 2, "3": 3}
		c, ok := classes[v]
		if !ok {
			return resFailed(cfg, sd, val, fmt.Errorf("unknown I/O scheduling class"))
		}
		st.ioClass = c
	default:
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 7 {
			return resFailed(cfg, sd, val, fmt.Errorf("I/O priority out of range 0..7"))
		}
		st.ioLevel = n
	}
	if st.ioClass == 0 {
		st.ioClass, st.ioLevel = 2, 4
	}
	st.ioSet = true
	names := [...]string{"", "realtime", "best-effort", "idle"}
	cfg.setMapped("ioprio", fmt.Sprintf("%s:%d", names[st.ioClass], st.ioLevel))
	return nil
}

// --- scheduling ---

// sdSchedPolicies are sched_policy_from_string's names and numbers that
// slinit has. SCHED_EXT ("ext", 7) it has not.
var sdSchedPolicies = map[string]int{
	"other": 0, "fifo": 1, "rr": 2, "batch": 3, "idle": 5,
	"0": 0, "1": 1, "2": 2, "3": 3, "5": 5,
}

var sdSchedNames = map[int]string{0: "other", 1: "fifo", 2: "rr", 3: "batch", 5: "idle"}

// resSched: CPUSchedulingPolicy= and CPUSchedulingPriority=. Setting the
// policy clamps the priority into its range — 1..99 for fifo and rr, 0
// for the rest — as config_parse_exec_cpu_sched_policy does, so a bare
// CPUSchedulingPolicy=fifo runs at priority 1. A priority without an
// rt policy is left for slinit's loader to refuse, as systemd fails the
// start at sched_setattr.
func resSched(cfg *SystemdConfig, sd, val string) []SystemdWarning {
	st := resState(cfg)
	v := strings.TrimSpace(trimQuotes(val))
	if sd == "CPUSchedulingPolicy" {
		if v == "" {
			st.schedSet, st.schedPolicy, st.schedPrio = false, 0, 0
			cfg.dropMapped("sched-policy")
			cfg.dropMapped("sched-priority")
			return nil
		}
		p, ok := sdSchedPolicies[v]
		if !ok {
			return resFailed(cfg, sd, val, fmt.Errorf("scheduling policy slinit does not know"))
		}
		st.schedPolicy = p
		if p == 1 || p == 2 {
			st.schedPrio = min(max(st.schedPrio, 1), 99)
		} else {
			st.schedPrio = 0
		}
	} else {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 99 {
			return resFailed(cfg, sd, val, fmt.Errorf("scheduling priority out of range 0..99"))
		}
		st.schedPrio = n
	}
	st.schedSet = true
	cfg.setMapped("sched-policy", sdSchedNames[st.schedPolicy])
	if st.schedPrio > 0 {
		cfg.setMapped("sched-priority", strconv.Itoa(st.schedPrio))
	} else {
		cfg.dropMapped("sched-priority")
	}
	return nil
}

// --- NUMA ---

var sdNUMAPolicies = map[string]bool{
	"default": true, "preferred": true, "bind": true, "interleave": true,
	"local": true, "preferred-many": true, "weighted-interleave": true,
}

// resNUMA: NUMAPolicy= and NUMAMask=. systemd applies the mask only with
// a policy and ignores it otherwise, where slinit refuses numa-nodes
// without numa-mempolicy; so the mask is emitted only once there is a
// policy.
func resNUMA(cfg *SystemdConfig, sd, val string) []SystemdWarning {
	st := resState(cfg)
	v := strings.TrimSpace(trimQuotes(val))
	var warns []SystemdWarning
	if sd == "NUMAPolicy" {
		if v != "" && !sdNUMAPolicies[v] {
			return resFailed(cfg, sd, val, fmt.Errorf("unknown NUMA policy"))
		}
		st.numaPolicy = v
	} else if v == "all" {
		n, err := sdNUMAMaxNode()
		if err != nil {
			// numa_mask_add_all assumes 1023 then; slinit's
			// numa-nodes would take that, but a mask of 1024 nodes
			// on a machine that has none is not what "all" means.
			return resFailed(cfg, sd, val, fmt.Errorf("cannot list this machine's NUMA nodes: %w", err))
		}
		st.numaMask = cpuSet{}
		for i := 0; i <= n; i++ {
			st.numaMask.add(uint(i))
		}
	} else {
		warns = st.numaMask.assign(sd, v)
	}
	if st.numaPolicy == "" {
		cfg.dropMapped("numa-mempolicy")
		cfg.dropMapped("numa-nodes")
		return warns
	}
	cfg.setMapped("numa-mempolicy", st.numaPolicy)
	setOrDrop(cfg, "numa-nodes", st.numaMask.String())
	return warns
}

// --- CPU sets ---

// cpuSet is a set of CPU (or NUMA node) numbers.
type cpuSet map[uint]struct{}

func (s *cpuSet) add(n uint) {
	if *s == nil {
		*s = cpuSet{}
	}
	(*s)[n] = struct{}{}
}

// assign is config_parse_cpu_set: an assignment adds to the set, an
// empty one empties it. A word that is no CPU or range is skipped with a
// warning and the rest kept, as systemd does.
func (s *cpuSet) assign(sd, v string) []SystemdWarning {
	if v == "" {
		*s = nil
		return nil
	}
	var warns []SystemdWarning
	for _, w := range strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}) {
		w = strings.Trim(w, `"'`)
		lo, hi, ok := sdRange(w)
		if !ok || hi > MaxCPUNumber {
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("[Service] %s=: %q is no CPU or range, ignored as systemd ignores it", sd, w)})
			continue
		}
		for n := lo; n <= hi; n++ {
			s.add(n)
		}
	}
	return warns
}

// sdRange is parse_range: "N" or "N-M" with N <= M.
func sdRange(w string) (uint, uint, bool) {
	a, b, isRange := strings.Cut(w, "-")
	lo, err := strconv.ParseUint(a, 10, 32)
	if err != nil {
		return 0, 0, false
	}
	hi := lo
	if isRange {
		if hi, err = strconv.ParseUint(b, 10, 32); err != nil || hi < lo {
			return 0, 0, false
		}
	}
	return uint(lo), uint(hi), true
}

// String is the kernel's cpulist form, "0-3,5", which cpuset.cpus
// needs (it takes no spaces) and cpu-affinity and numa-nodes accept.
func (s cpuSet) String() string {
	ns := make([]uint, 0, len(s))
	for n := range s {
		ns = append(ns, n)
	}
	sort.Slice(ns, func(i, j int) bool { return ns[i] < ns[j] })
	var parts []string
	for i := 0; i < len(ns); {
		j := i
		for j+1 < len(ns) && ns[j+1] == ns[j]+1 {
			j++
		}
		if i == j {
			parts = append(parts, strconv.FormatUint(uint64(ns[i]), 10))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", ns[i], ns[j]))
		}
		i = j + 1
	}
	return strings.Join(parts, ",")
}

// resCPUSet: CPUAffinity=, AllowedCPUs=, AllowedMemoryNodes= and their
// Startup forms. A Startup set maps to slinit's startup-allowed-*, which
// differs from systemd twice over: it holds until the service is
// started, not until the system has booted (nor again at shutdown), and
// it falls back only to an explicit cgroup-cpuset-*, keeping the startup
// set for good without one, where systemd lifts the restriction. So it
// is emitted only together with the steady set.
func resCPUSet(cfg *SystemdConfig, sd, val string) []SystemdWarning {
	st := resState(cfg)
	v := strings.TrimSpace(trimQuotes(val))
	var warns []SystemdWarning
	switch sd {
	case "CPUAffinity":
		if v == "numa" {
			st.affinityFromNUMA = true
			st.affinity = nil
			warns = append(warns, SystemdWarning{"WARN", "[Service] CPUAffinity=numa ignored: slinit cannot derive " +
				"the affinity from NUMAMask=; list the CPUs of those nodes instead"})
		} else {
			warns = st.affinity.assign(sd, v)
			st.affinityFromNUMA = false
		}
		if st.affinityFromNUMA {
			cfg.dropMapped("cpu-affinity")
		} else {
			setOrDrop(cfg, "cpu-affinity", st.affinity.String())
		}
		return warns
	case "AllowedCPUs":
		warns = st.cpus.assign(sd, v)
	case "AllowedMemoryNodes":
		warns = st.mems.assign(sd, v)
	case "StartupAllowedCPUs":
		warns = st.startupCPUs.assign(sd, v)
	case "StartupAllowedMemoryNodes":
		warns = st.startupMems.assign(sd, v)
	}
	emitted := false
	render := func(steadyKey, startupKey string, steady, startup cpuSet, sdSteady, sdStartup string) {
		setOrDrop(cfg, steadyKey, steady.String())
		if len(startup) > 0 && len(steady) == 0 && sd == sdStartup {
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("[Service] %s=%s dropped unless the unit also sets %s=: "+
				"slinit would keep the startup set for the service's whole life, where systemd lifts it once the system has booted", sd, v, sdSteady)})
		}
		if len(startup) > 0 && len(steady) > 0 {
			cfg.setMapped(startupKey, startup.String())
			if sd == sdStartup || sd == sdSteady {
				warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("%s= holds in slinit until the service is started, "+
					"not until the system has booted, and is not applied again at shutdown", sd)})
			}
		} else {
			cfg.dropMapped(startupKey)
		}
		emitted = emitted || len(steady) > 0
	}
	render("cgroup-cpuset-cpus", "startup-allowed-cpus", st.cpus, st.startupCPUs, "AllowedCPUs", "StartupAllowedCPUs")
	render("cgroup-cpuset-mems", "startup-allowed-memory-nodes", st.mems, st.startupMems, "AllowedMemoryNodes", "StartupAllowedMemoryNodes")
	if emitted {
		warns = append(warns, cgroupNote(cfg, st)...)
	}
	return warns
}

// --- process attributes ---

// sdCoredumpBits are coredump_filter_table's names, by bit.
var sdCoredumpBits = map[string]uint{
	"private-anonymous": 0, "shared-anonymous": 1, "private-file-backed": 2, "shared-file-backed": 3,
	"elf-headers": 4, "private-huge": 5, "shared-huge": 6, "private-dax": 7, "shared-dax": 8,
}

// resCoredumpFilter: assignments OR together, an empty one resets
// (config_parse_exec_coredump_filter). A bare number is hex in systemd
// (safe_atoux64), so the mask is always emitted with 0x: the kernel
// reads /proc/self/coredump_filter with base 0, where "33" is decimal.
func resCoredumpFilter(cfg *SystemdConfig, sd, val string) []SystemdWarning {
	st := resState(cfg)
	v := strings.TrimSpace(trimQuotes(val))
	if v == "" {
		st.coredumpMask, st.coredumpSet = 0, false
		cfg.dropMapped("coredump-filter")
		return nil
	}
	var m uint64
	for _, w := range strings.Fields(v) {
		switch {
		case w == "default":
			// COREDUMP_FILTER_MASK_DEFAULT: the kernel's 0x33.
			m |= 1<<0 | 1<<1 | 1<<4 | 1<<5
		case w == "all":
			m = math.MaxUint32
		default:
			if b, ok := sdCoredumpBits[w]; ok {
				m |= 1 << b
				continue
			}
			x, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(w), "0x"), 16, 64)
			if err != nil {
				return resFailed(cfg, sd, val, fmt.Errorf("%q is no coredump filter name or hex mask", w))
			}
			m |= x
		}
	}
	if st.coredumpMask|m > math.MaxUint32 {
		return resFailed(cfg, sd, val, fmt.Errorf("mask wider than 32 bits"))
	}
	st.coredumpMask |= m
	st.coredumpSet = true
	cfg.setMapped("coredump-filter", fmt.Sprintf("0x%x", st.coredumpMask))
	return nil
}

// convMemoryTHP: systemd's values are inherit, disable, madvise and
// system. disable is slinit's never. inherit and system both leave the
// policy slinit runs with, which is the system's (slinit never disables
// THP for itself). madvise means PR_THP_DISABLE_EXCEPT_ADVISED, which
// slinit does not implement: its memory-thp=madvise is a no-op.
func convMemoryTHP(v string) (string, []SystemdWarning, error) {
	switch v {
	case "", "inherit", "system":
		return "", nil, nil
	case "disable":
		return "never", nil, nil
	case "madvise":
		return "", nil, fmt.Errorf("slinit cannot disable THP except where madvise()d (PR_THP_DISABLE_EXCEPT_ADVISED); " +
			"memory-thp=never disables it entirely")
	}
	return "", nil, fmt.Errorf("unknown MemoryTHP= value")
}

func convCPUSetPartition(v string) (string, []SystemdWarning, error) {
	switch v {
	case "":
		return "", nil, nil
	case "member", "root", "isolated":
		return v, nil, nil
	}
	return "", nil, fmt.Errorf("unknown cpuset partition type")
}

// convDirectoryQuota: a size becomes plain bytes. systemd's percentage
// of the file system has no slinit form.
func convDirectoryQuota(v string) (string, []SystemdWarning, error) {
	if v == "" || v == "off" {
		return "", nil, nil
	}
	if _, isPct, err := sdPermyriad(v, true); isPct || err != nil {
		return "", nil, fmt.Errorf("slinit takes the quota in bytes only, not as a share of the file system")
	}
	n, err := sdParseSize(v)
	if err != nil {
		return "", nil, err
	}
	if n > math.MaxInt64 {
		return "", nil, fmt.Errorf("quota out of range")
	}
	return strconv.FormatUint(n, 10), []SystemdWarning{{"NOTE",
		"slinit records directory quotas but does not enforce them yet"}}, nil
}
