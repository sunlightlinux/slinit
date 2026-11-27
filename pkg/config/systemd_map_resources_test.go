package config

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// fixHost pins the host facts the percentage and "all" forms are
// computed from, so the expected values do not depend on the machine
// the tests run on.
func fixHost(t *testing.T) {
	t.Helper()
	mem, tasks, numa := sdPhysicalMemory, sdSystemTasksMax, sdNUMAMaxNode
	t.Cleanup(func() { sdPhysicalMemory, sdSystemTasksMax, sdNUMAMaxNode = mem, tasks, numa })
	sdPhysicalMemory = func() (uint64, error) { return 8 << 30, nil }
	sdSystemTasksMax = func() (uint64, error) { return 32767, nil }
	sdNUMAMaxNode = func() (int, error) { return 1, nil }
}

// convertService converts a [Service] body and returns the emitted
// slinit lines and the warnings.
func convertService(t *testing.T, body string) ([]string, []SystemdWarning) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "probe.service")
	if err := os.WriteFile(p, []byte("[Service]\nExecStart=/bin/true\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, warns, err := ConvertSystemdUnit(p)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	var buf bytes.Buffer
	EmitSlinitFile(&buf, cfg)
	if _, err := Parse(bytes.NewReader(buf.Bytes()), "probe", p); err != nil {
		t.Fatalf("emitted file does not parse: %v\n%s", err, buf.String())
	}
	return strings.Split(buf.String(), "\n"), warns
}

func TestSystemdResourceMappings(t *testing.T) {
	fixHost(t)
	const sl = "Slice=system.slice\n" // a cgroup of its own: no cgroupNote
	cases := []struct {
		name, body string
		want       []string // emitted lines
		absent     []string // keys (or "key = file") that must not be emitted
		warn       string   // a WARN must contain this; "" means no WARN
	}{
		// memory: sizes, percentages, infinity, reset
		{"MemoryMax size", sl + "MemoryMax=512M\n", []string{"cgroup-memory-max = 536870912"}, nil, ""},
		{"MemoryMax fraction", sl + "MemoryMax=1.5G\n", []string{"cgroup-memory-max = 1610612736"}, nil, ""},
		{"MemoryMax sum", sl + "MemoryMax=1G 512M\n", []string{"cgroup-memory-max = 1610612736"}, nil, ""},
		{"MemoryMax bytes", sl + "MemoryMax=4096\n", []string{"cgroup-memory-max = 4096"}, nil, ""},
		{"MemoryMax percent", sl + "MemoryMax=50%\n", []string{"cgroup-memory-max = 4294967296"}, nil, ""},
		{"MemoryMax permille", sl + "MemoryMax=125‰\n", []string{"cgroup-memory-max = 1073741824"}, nil, ""},
		{"MemoryHigh two decimals", sl + "MemoryHigh=12.5%\n", []string{"cgroup-memory-high = 1073741824"}, nil, ""},
		{"MemoryMax infinity", sl + "MemoryMax=infinity\n", nil, []string{"cgroup-memory-max"}, ""},
		{"MemoryMax reset", sl + "MemoryMax=1G\nMemoryMax=\n", nil, []string{"cgroup-memory-max"}, ""},
		{"MemoryMax last wins", sl + "MemoryMax=1G\nMemoryMax=2G\n", []string{"cgroup-memory-max = 2147483648"}, nil, ""},
		{"MemoryMax zero", sl + "MemoryMax=0\n", nil, []string{"cgroup-memory-max"}, "MemoryMax=0 ignored"},
		{"MemoryMax lower-case suffix", sl + "MemoryMax=512m\n", nil, []string{"cgroup-memory-max"}, "MemoryMax=512m ignored"},
		{"MemoryMax over 100%", sl + "MemoryMax=150%\n", nil, []string{"cgroup-memory-max"}, "MemoryMax=150% ignored"},
		{"MemorySwapMax zero", sl + "MemorySwapMax=0\n", []string{"cgroup-swap-max = 0"}, nil, ""},
		{"MemoryLow infinity", sl + "MemoryLow=infinity\n", []string{"cgroup-memory-low = max"}, nil, ""},
		{"MemoryMin zero is the default", sl + "MemoryMin=0\n", nil, []string{"cgroup-memory-min"}, ""},
		{"MemoryZSwapMax", sl + "MemoryZSwapMax=1G\nMemoryZSwapWriteback=no\n",
			[]string{"cgroup-setting = memory.zswap.max 1073741824", "cgroup-setting = memory.zswap.writeback 0"}, nil, ""},
		{"MemoryZSwapWriteback yes is the default", sl + "MemoryZSwapWriteback=no\nMemoryZSwapWriteback=yes\n",
			nil, []string{"cgroup-setting = memory.zswap.writeback"}, ""},

		// MemoryLimit= as systemd 257 applied it
		{"MemoryLimit alone", sl + "MemoryLimit=1G\n", []string{"cgroup-memory-max = 1073741824"}, nil, ""},
		{"MemoryLimit then MemoryMax", sl + "MemoryLimit=1G\nMemoryMax=2G\n", []string{"cgroup-memory-max = 2147483648"}, nil, ""},
		{"MemoryMax then MemoryLimit", sl + "MemoryMax=2G\nMemoryLimit=1G\n", []string{"cgroup-memory-max = 2147483648"}, nil, ""},
		{"MemoryLimit yields to MemoryHigh", sl + "MemoryLimit=1G\nMemoryHigh=512M\n",
			[]string{"cgroup-memory-high = 536870912"}, []string{"cgroup-memory-max"}, ""},
		{"MemoryLimit back after MemoryHigh reset", sl + "MemoryLimit=1G\nMemoryHigh=512M\nMemoryHigh=\n",
			[]string{"cgroup-memory-max = 1073741824"}, []string{"cgroup-memory-high"}, ""},

		// tasks
		{"TasksMax", sl + "TasksMax=100\n", []string{"cgroup-pids-max = 100"}, nil, ""},
		{"TasksMax percent", sl + "TasksMax=15%\n", []string{"cgroup-pids-max = 4915"}, nil, ""},
		{"TasksMax infinity", sl + "TasksMax=100\nTasksMax=infinity\n", nil, []string{"cgroup-pids-max"}, ""},
		{"TasksMax zero", sl + "TasksMax=0\n", nil, []string{"cgroup-pids-max"}, "TasksMax=0 ignored"},

		// CPU weight, CPUShares= as systemd 257 applied it
		{"CPUWeight", sl + "CPUWeight=200\n", []string{"cgroup-cpu-weight = 200"}, nil, ""},
		{"CPUWeight idle", sl + "CPUWeight=idle\n", []string{"cgroup-setting = cpu.idle 1"}, []string{"cgroup-cpu-weight"}, ""},
		{"CPUWeight idle then number", sl + "CPUWeight=idle\nCPUWeight=50\n",
			[]string{"cgroup-cpu-weight = 50"}, []string{"cgroup-setting = cpu.idle"}, ""},
		{"CPUWeight out of range", sl + "CPUWeight=10001\n", nil, []string{"cgroup-cpu-weight"}, "CPUWeight=10001 ignored"},
		{"CPUShares", sl + "CPUShares=2048\n", []string{"cgroup-cpu-weight = 200"}, nil, ""},
		{"CPUShares clamps to 1", sl + "CPUShares=2\n", []string{"cgroup-cpu-weight = 1"}, nil, ""},
		{"CPUShares yields to CPUWeight", sl + "CPUWeight=50\nCPUShares=2048\n", []string{"cgroup-cpu-weight = 50"}, nil, ""},
		{"CPUWeight reset uncovers CPUShares", sl + "CPUShares=2048\nCPUWeight=50\nCPUWeight=\n",
			[]string{"cgroup-cpu-weight = 200"}, nil, ""},

		// cpu.max
		{"CPUQuota", sl + "CPUQuota=50%\n", []string{"cgroup-cpu-max = 50000 100000"}, nil, ""},
		{"CPUQuota over one CPU", sl + "CPUQuota=200%\n", []string{"cgroup-cpu-max = 200000 100000"}, nil, ""},
		{"CPUQuota then period", sl + "CPUQuota=50%\nCPUQuotaPeriodSec=10ms\n", []string{"cgroup-cpu-max = 5000 10000"}, nil, ""},
		{"period then CPUQuota", sl + "CPUQuotaPeriodSec=10ms\nCPUQuota=50%\n", []string{"cgroup-cpu-max = 5000 10000"}, nil, ""},
		{"period clamped up for a small quota", sl + "CPUQuotaPeriodSec=500us\nCPUQuota=50%\n", []string{"cgroup-cpu-max = 1000 2000"}, nil, ""},
		{"tiny quota", sl + "CPUQuota=0.05%\n", []string{"cgroup-cpu-max = 1000 1000000"}, nil, ""},
		{"period alone", sl + "CPUQuotaPeriodSec=10ms\n", nil, []string{"cgroup-cpu-max"}, ""},
		{"CPUQuota reset", sl + "CPUQuota=50%\nCPUQuota=\n", nil, []string{"cgroup-cpu-max"}, ""},
		{"CPUQuota zero", sl + "CPUQuota=0%\n", nil, []string{"cgroup-cpu-max"}, "CPUQuota=0% ignored"},
		{"CPUQuota no percent", sl + "CPUQuota=50\n", nil, []string{"cgroup-cpu-max"}, "CPUQuota=50 ignored"},

		// io.weight, BlockIOWeight= as systemd 257 applied it
		{"IOWeight", sl + "IOWeight=500\n", []string{"cgroup-io-weight = 500"}, nil, ""},
		{"BlockIOWeight", sl + "BlockIOWeight=1000\n", []string{"cgroup-io-weight = 200"}, nil, ""},
		{"BlockIOWeight yields to IOWeight", sl + "BlockIOWeight=1000\nIOWeight=50\n", []string{"cgroup-io-weight = 50"}, nil, ""},
		{"BlockIOWeight out of range", sl + "BlockIOWeight=5\n", nil, []string{"cgroup-io-weight"}, "BlockIOWeight=5 ignored"},

		// ioprio
		{"IOSchedulingClass alone", "IOSchedulingClass=idle\n", []string{"ioprio = idle:4"}, nil, ""},
		{"IOSchedulingPriority alone", "IOSchedulingPriority=7\n", []string{"ioprio = best-effort:7"}, nil, ""},
		{"class then priority", "IOSchedulingClass=realtime\nIOSchedulingPriority=2\n", []string{"ioprio = realtime:2"}, nil, ""},
		{"priority then class", "IOSchedulingPriority=2\nIOSchedulingClass=realtime\n", []string{"ioprio = realtime:2"}, nil, ""},
		{"class none is the default", "IOSchedulingPriority=2\nIOSchedulingClass=none\n", []string{"ioprio = best-effort:4"}, nil, ""},
		{"ioprio reset", "IOSchedulingClass=idle\nIOSchedulingPriority=\n", nil, []string{"ioprio"}, ""},
		{"bad class", "IOSchedulingClass=fast\n", nil, []string{"ioprio"}, "IOSchedulingClass=fast ignored"},
		{"bad priority", "IOSchedulingPriority=8\n", nil, []string{"ioprio"}, "IOSchedulingPriority=8 ignored"},

		// scheduling
		{"fifo alone runs at 1", "CPUSchedulingPolicy=fifo\n", []string{"sched-policy = fifo", "sched-priority = 1"}, nil, ""},
		{"rr and priority", "CPUSchedulingPriority=50\nCPUSchedulingPolicy=rr\n", []string{"sched-policy = rr", "sched-priority = 50"}, nil, ""},
		{"other clamps priority", "CPUSchedulingPriority=50\nCPUSchedulingPolicy=other\n",
			[]string{"sched-policy = other"}, []string{"sched-priority"}, ""},
		{"batch", "CPUSchedulingPolicy=batch\n", []string{"sched-policy = batch"}, nil, ""},
		{"policy reset", "CPUSchedulingPolicy=fifo\nCPUSchedulingPolicy=\n", nil, []string{"sched-policy", "sched-priority"}, ""},
		{"ext", "CPUSchedulingPolicy=ext\n", nil, []string{"sched-policy"}, "CPUSchedulingPolicy=ext ignored"},
		{"slinit alias is not systemd's", "CPUSchedulingPolicy=realtime\n", nil, []string{"sched-policy"}, "ignored"},
		{"reset-on-fork", "CPUSchedulingResetOnFork=on\n", []string{"sched-reset-on-fork = yes"}, nil, ""},
		{"reset-on-fork no", "CPUSchedulingResetOnFork=no\n", nil, []string{"sched-reset-on-fork"}, ""},
		{"reset-on-fork bad", "CPUSchedulingResetOnFork=maybe\n", nil, []string{"sched-reset-on-fork"}, "ignored"},

		// NUMA
		{"NUMA", "NUMAPolicy=bind\nNUMAMask=0-1\n", []string{"numa-mempolicy = bind", "numa-nodes = 0-1"}, nil, ""},
		{"NUMA mask first", "NUMAMask=0 1\nNUMAPolicy=interleave\n", []string{"numa-mempolicy = interleave", "numa-nodes = 0-1"}, nil, ""},
		{"NUMAMask alone does nothing", "NUMAMask=0\n", nil, []string{"numa-nodes", "numa-mempolicy"}, ""},
		{"NUMAMask all", "NUMAPolicy=bind\nNUMAMask=all\n", []string{"numa-nodes = 0-1"}, nil, ""},
		{"NUMAPolicy local", "NUMAPolicy=local\n", []string{"numa-mempolicy = local"}, []string{"numa-nodes"}, ""},
		{"NUMAPolicy bad", "NUMAPolicy=spread\n", nil, []string{"numa-mempolicy"}, "NUMAPolicy=spread ignored"},

		// CPU sets
		{"CPUAffinity adds up", "CPUAffinity=0 2-3\nCPUAffinity=5\n", []string{"cpu-affinity = 0,2-3,5"}, nil, ""},
		{"CPUAffinity reset", "CPUAffinity=0\nCPUAffinity=\nCPUAffinity=1\n", []string{"cpu-affinity = 1"}, nil, ""},
		{"CPUAffinity bad word skipped", "CPUAffinity=0,x,2\n", []string{"cpu-affinity = 0,2"}, nil, `"x" is no CPU`},
		{"CPUAffinity numa", "CPUAffinity=numa\n", nil, []string{"cpu-affinity"}, "CPUAffinity=numa ignored"},
		{"AllowedCPUs", sl + "AllowedCPUs=0 2\n", []string{"cgroup-cpuset-cpus = 0,2"}, nil, ""},
		{"AllowedMemoryNodes", sl + "AllowedMemoryNodes=0-1\n", []string{"cgroup-cpuset-mems = 0-1"}, nil, ""},
		{"StartupAllowedCPUs alone", sl + "StartupAllowedCPUs=0\n", nil, []string{"startup-allowed-cpus", "cgroup-cpuset-cpus"}, "dropped unless"},
		{"StartupAllowedCPUs with steady", sl + "StartupAllowedCPUs=0\nAllowedCPUs=0-3\n",
			[]string{"startup-allowed-cpus = 0", "cgroup-cpuset-cpus = 0-3"}, nil, "dropped unless"},
		{"steady then StartupAllowedCPUs", sl + "AllowedCPUs=0-3\nStartupAllowedCPUs=0\n",
			[]string{"startup-allowed-cpus = 0", "cgroup-cpuset-cpus = 0-3"}, nil, ""},
		{"StartupAllowedMemoryNodes with steady", sl + "AllowedMemoryNodes=0-1\nStartupAllowedMemoryNodes=0\n",
			[]string{"startup-allowed-memory-nodes = 0", "cgroup-cpuset-mems = 0-1"}, nil, ""},
		{"CPUSetPartition", sl + "CPUSetPartition=root\n", []string{"cpuset-partition = root"}, nil, ""},
		{"CPUSetPartition bad", sl + "CPUSetPartition=shared\n", nil, []string{"cpuset-partition"}, "ignored"},

		// process attributes
		{"CoredumpFilter default", "CoredumpFilter=default\n", []string{"coredump-filter = 0x33"}, nil, ""},
		{"CoredumpFilter names add up", "CoredumpFilter=private-anonymous\nCoredumpFilter=shared-dax\n", []string{"coredump-filter = 0x101"}, nil, ""},
		{"CoredumpFilter number is hex", "CoredumpFilter=33\n", []string{"coredump-filter = 0x33"}, nil, ""},
		{"CoredumpFilter all", "CoredumpFilter=all\n", []string{"coredump-filter = 0xffffffff"}, nil, ""},
		{"CoredumpFilter reset", "CoredumpFilter=all\nCoredumpFilter=\n", nil, []string{"coredump-filter"}, ""},
		{"CoredumpFilter bad", "CoredumpFilter=stack\n", nil, []string{"coredump-filter"}, "CoredumpFilter=stack ignored"},
		{"MemoryKSM", "MemoryKSM=yes\n", []string{"memory-ksm = yes"}, nil, ""},
		{"MemoryKSM unset", "MemoryKSM=yes\nMemoryKSM=\n", nil, []string{"memory-ksm"}, ""},
		{"MemoryTHP disable", "MemoryTHP=disable\n", []string{"memory-thp = never"}, nil, ""},
		{"MemoryTHP system", "MemoryTHP=disable\nMemoryTHP=system\n", nil, []string{"memory-thp"}, ""},
		{"MemoryTHP madvise", "MemoryTHP=madvise\n", nil, []string{"memory-thp"}, "MemoryTHP=madvise ignored"},
		{"directory quota", "CacheDirectoryQuota=1G\n", []string{"cache-directory-quota = 1073741824"}, nil, ""},
		{"directory quota percent", "StateDirectoryQuota=10%\n", nil, []string{"state-directory-quota"}, "ignored"},
		{"directory quota off", "LogsDirectoryQuota=1G\nLogsDirectoryQuota=off\n", nil, []string{"logs-directory-quota"}, ""},
		{"directory accounting", "StateDirectoryAccounting=yes\n", []string{"state-directory-accounting = yes"}, nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lines, warns := convertService(t, c.body)
			for _, w := range c.want {
				if !slices.Contains(lines, w) {
					t.Errorf("missing %q in:\n%s", w, strings.Join(lines, "\n"))
				}
			}
			for _, a := range c.absent {
				for _, l := range lines {
					if strings.HasPrefix(l, a+" ") || strings.HasPrefix(l, a+" =") {
						t.Errorf("unexpected %q", l)
					}
				}
			}
			var got []string
			for _, w := range warns {
				if w.Level == "WARN" {
					got = append(got, w.Msg)
				}
			}
			switch {
			case c.warn == "" && len(got) > 0:
				t.Errorf("unexpected WARN: %q", got)
			case c.warn != "" && !slices.ContainsFunc(got, func(m string) bool { return strings.Contains(m, c.warn) }):
				t.Errorf("no WARN containing %q in %q", c.warn, got)
			}
		})
	}
}

// The emitted lines reach the service description slinit runs: the
// direct loader's path, SystemdUnitToServiceDescription.
func TestSystemdResourceDescription(t *testing.T) {
	fixHost(t)
	p := filepath.Join(t.TempDir(), "probe.service")
	body := "[Service]\nExecStart=/bin/true\nSlice=system.slice\n" +
		"MemoryMax=512M\nMemoryZSwapMax=1G\nTasksMax=100\nCPUQuota=50%\nCPUWeight=idle\n" +
		"AllowedCPUs=0-3\nStartupAllowedCPUs=0\n" +
		"IOSchedulingClass=best-effort\nIOSchedulingPriority=6\n" +
		"CPUSchedulingPolicy=fifo\nCPUAffinity=1,3\nNUMAPolicy=bind\nNUMAMask=0\nCoredumpFilter=default\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	desc, err := SystemdUnitToServiceDescription(p, "probe")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []CgroupSetting{
		{"memory.max", "536870912"}, {"memory.zswap.max", "1073741824"}, {"pids.max", "100"},
		{"cpu.max", "50000 100000"}, {"cpu.idle", "1"}, {"cpuset.cpus", "0-3"},
	} {
		if !slices.Contains(desc.CgroupSettings, want) {
			t.Errorf("CgroupSettings %v lacks %v", desc.CgroupSettings, want)
		}
	}
	if desc.StartupAllowedCPUs != "0" {
		t.Errorf("StartupAllowedCPUs = %q", desc.StartupAllowedCPUs)
	}
	if desc.IOPrio != "best-effort:6" {
		t.Errorf("IOPrio = %q", desc.IOPrio)
	}
	if c, l := parseIOPrio(desc.IOPrio); c != 2 || l != 6 {
		t.Errorf("parseIOPrio(%q) = %d:%d", desc.IOPrio, c, l)
	}
	if !desc.SchedPolicySet || desc.SchedPolicy != 1 || desc.SchedPriority != 1 {
		t.Errorf("sched = %v/%d/%d, want SCHED_FIFO at 1", desc.SchedPolicySet, desc.SchedPolicy, desc.SchedPriority)
	}
	if !slices.Equal(desc.CPUAffinity, []uint{1, 3}) {
		t.Errorf("CPUAffinity = %v", desc.CPUAffinity)
	}
	if !desc.NumaMempolicySet || !slices.Equal(desc.NumaNodes, []uint{0}) {
		t.Errorf("NUMA = %v %v", desc.NumaMempolicySet, desc.NumaNodes)
	}
	if desc.CoredumpFilter != "0x33" {
		t.Errorf("CoredumpFilter = %q", desc.CoredumpFilter)
	}
}

// Without slice= or cgroup= the cgroup settings land in the daemon's
// shared --cgroup-path or nowhere: one WARN per unit says so, and none
// once the unit has a Slice=.
func TestSystemdResourceNeedsOwnCgroup(t *testing.T) {
	fixHost(t)
	count := func(warns []SystemdWarning) int {
		n := 0
		for _, w := range warns {
			if strings.Contains(w.Msg, "cgroup of the service's own") {
				n++
			}
		}
		return n
	}
	if _, w := convertService(t, "MemoryMax=1G\nTasksMax=10\nCPUWeight=20\n"); count(w) != 1 {
		t.Errorf("no Slice=: %d notes, want 1: %v", count(w), w)
	}
	if _, w := convertService(t, "Slice=system.slice\nMemoryMax=1G\n"); count(w) != 0 {
		t.Errorf("with Slice=: %d notes, want 0", count(w))
	}
	// Slice= after the settings (or in a drop-in) counts too: the check
	// runs once the whole unit is read.
	if _, w := convertService(t, "MemoryMax=1G\nSlice=system.slice\n"); count(w) != 0 {
		t.Errorf("with a later Slice=: %d notes, want 0", count(w))
	}
	if _, w := convertService(t, "CPUAffinity=0\nIOSchedulingClass=idle\n"); count(w) != 0 {
		t.Errorf("process attributes need no cgroup, got %d notes", count(w))
	}
}

// The gaps and ignored directives answer as classified: a confining gap
// with a WARN, a behavioural one with a NOTE, an ignored one silently.
func TestSystemdResourceGapsAndIgnored(t *testing.T) {
	for _, c := range []struct{ body, level, text string }{
		{"IOReadBandwidthMax=/dev/sda 1M\n", "NOTE", "MAJ:MIN"},
		{"BlockIOReadBandwidth=/dev/sda 1M\n", "NOTE", "removed in systemd 258"},
		{"DeviceAllow=/dev/null rw\n", "WARN", "device cgroup"},
		{"IPAddressDeny=any\n", "WARN", "IP filter"},
		{"LimitNPROC=1\n", "WARN", "RLIMIT_NPROC"},
		{"LimitCPU=10\n", "NOTE", "rlimit-nofile"},
		{"StartupCPUWeight=20\n", "NOTE", "boots"},
		{"MemoryPressureWatch=yes\n", "NOTE", "PRESSURE_WATCH"},
		{"ManagedOOMSwap=kill\n", "NOTE", "OOM"},
	} {
		_, warns := convertService(t, c.body)
		if !slices.ContainsFunc(warns, func(w SystemdWarning) bool {
			return w.Level == c.level && strings.Contains(w.Msg, c.text)
		}) {
			t.Errorf("%s: want a %s containing %q, got %v", strings.TrimSpace(c.body), c.level, c.text, warns)
		}
	}
	for _, body := range []string{"MemoryAccounting=yes\n", "CPUAccounting=yes\n", "NetClass=auto\n", "DefaultMemoryLow=1G\n"} {
		if _, warns := convertService(t, body); len(warns) != 0 {
			t.Errorf("%s: ignored, want no warning, got %v", strings.TrimSpace(body), warns)
		}
	}
}

// The value grammars against the systemd parsers they copy.
func TestSystemdResourceGrammars(t *testing.T) {
	for in, want := range map[string]uint64{
		"0": 0, "1K": 1024, "1.5K": 1536, "10.M": 10 << 20, "1G 512M": 1<<30 + 512<<20, "2B": 2, "1E": 1 << 60,
	} {
		if got, err := sdParseSize(in); err != nil || got != want {
			t.Errorf("sdParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "1k", "512M 1G", "1G 1G", "-1", "1Gx", "x", "20E"} {
		if got, err := sdParseSize(in); err == nil {
			t.Errorf("sdParseSize(%q) = %d, want an error", in, got)
		}
	}
	for in, want := range map[string]int{"50%": 5000, "12.5%": 1250, "12.34%": 1234, "5‰": 50, "5.5‰": 55, "7‱": 7, "0%": 0, "100%": 10000} {
		if got, ok, err := sdPermyriad(in, true); err != nil || !ok || got != want {
			t.Errorf("sdPermyriad(%q) = %d, %v, %v; want %d", in, got, ok, err, want)
		}
	}
	for _, in := range []string{"05%", "+5%", "1.234%", "5.%", "1.5‱", "101%", "x%"} {
		if _, ok, err := sdPermyriad(in, true); !ok || err == nil {
			t.Errorf("sdPermyriad(%q): want an error", in)
		}
	}
	if _, ok, _ := sdPermyriad("512M", true); ok {
		t.Errorf("sdPermyriad(512M) claims a percentage")
	}
	for in, want := range map[string]uint64{"10ms": 10000, "1s": 1e6, "1": 1e6, "500us": 500, "1.5s": 1500000} {
		if got, ok := sdTimespanUsec(in); !ok || got != want {
			t.Errorf("sdTimespanUsec(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
}

// The cross-field rules of slinit's loader (validateDesc) are where a
// mapping of scheduling or NUMA settings would turn a unit systemd runs
// into one slinit refuses: a bare CPUSchedulingPolicy=fifo (systemd runs
// it at priority 1) and a NUMAMask= without NUMAPolicy= (systemd ignores
// the mask). Both must load; a priority without an rt policy, which
// systemd fails at sched_setattr, is refused.
func TestSystemdResourceLoaderValidation(t *testing.T) {
	fixHost(t)
	dl := NewDirLoader(service.NewServiceSet(&testConsumerLogger{}), nil)
	for body, ok := range map[string]bool{
		"CPUSchedulingPolicy=fifo\n":                         true,
		"CPUSchedulingPriority=20\nCPUSchedulingPolicy=rr\n": true,
		"NUMAMask=0\n":                  true,
		"NUMAPolicy=bind\nNUMAMask=0\n": true,
		"CPUSchedulingPriority=20\n":    false,
	} {
		p := writeUnit(t, t.TempDir(), "probe.service", "[Service]\nExecStart=/bin/true\n"+body)
		desc, err := SystemdUnitToServiceDescription(p, "probe")
		if err != nil {
			t.Fatalf("%s: %v", strings.TrimSpace(body), err)
		}
		if err := dl.validateDesc("probe", desc); (err == nil) != ok {
			t.Errorf("%s: validateDesc = %v, want ok=%v", strings.TrimSpace(body), err, ok)
		}
	}
}
