package config

import (
	"strings"
	"testing"
)

func TestParseCgroupMemoryMax(t *testing.T) {
	input := `type = process
command = /bin/app
cgroup = /sys/fs/cgroup/app
cgroup-memory-max = 536870912
`
	desc, err := Parse(strings.NewReader(input), "app", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.CgroupPath != "/sys/fs/cgroup/app" {
		t.Errorf("CgroupPath = %q", desc.CgroupPath)
	}
	if len(desc.CgroupSettings) != 1 {
		t.Fatalf("expected 1 setting, got %d", len(desc.CgroupSettings))
	}
	if desc.CgroupSettings[0].File != "memory.max" || desc.CgroupSettings[0].Value != "536870912" {
		t.Errorf("setting = %+v", desc.CgroupSettings[0])
	}
}

func TestParseCgroupMultipleSettings(t *testing.T) {
	input := `type = process
command = /bin/app
cgroup = /sys/fs/cgroup/app
cgroup-memory-max = 1G
cgroup-pids-max = 100
cgroup-cpu-weight = 50
cgroup-io-weight = 200
cgroup-cpuset-cpus = 0-3
cgroup-cpuset-mems = 0
`
	desc, err := Parse(strings.NewReader(input), "app", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	expected := map[string]string{
		"memory.max":  "1G",
		"pids.max":    "100",
		"cpu.weight":  "50",
		"io.weight":   "200",
		"cpuset.cpus": "0-3",
		"cpuset.mems": "0",
	}
	if len(desc.CgroupSettings) != len(expected) {
		t.Fatalf("expected %d settings, got %d", len(expected), len(desc.CgroupSettings))
	}
	for _, s := range desc.CgroupSettings {
		want, ok := expected[s.File]
		if !ok {
			t.Errorf("unexpected setting file %q", s.File)
			continue
		}
		if s.Value != want {
			t.Errorf("%s = %q, want %q", s.File, s.Value, want)
		}
	}
}

func TestParseCgroupGenericSetting(t *testing.T) {
	input := `type = process
command = /bin/app
cgroup = /sys/fs/cgroup/app
cgroup-setting = io.latency target=10000
`
	desc, err := Parse(strings.NewReader(input), "app", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(desc.CgroupSettings) != 1 {
		t.Fatalf("expected 1 setting, got %d", len(desc.CgroupSettings))
	}
	if desc.CgroupSettings[0].File != "io.latency" || desc.CgroupSettings[0].Value != "target=10000" {
		t.Errorf("setting = %+v", desc.CgroupSettings[0])
	}
}

func TestParseCgroupHugetlb(t *testing.T) {
	input := `type = process
command = /bin/app
cgroup = /sys/fs/cgroup/app
cgroup-hugetlb = 2MB 4
`
	desc, err := Parse(strings.NewReader(input), "app", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(desc.CgroupSettings) != 1 {
		t.Fatalf("expected 1 setting, got %d", len(desc.CgroupSettings))
	}
	if desc.CgroupSettings[0].File != "hugetlb.2MB.max" || desc.CgroupSettings[0].Value != "4" {
		t.Errorf("setting = %+v", desc.CgroupSettings[0])
	}
}

func TestParseCgroupMemoryAllLevels(t *testing.T) {
	input := `type = process
command = /bin/app
cgroup = /sys/fs/cgroup/app
cgroup-memory-max = 1G
cgroup-memory-high = 800M
cgroup-memory-low = 100M
cgroup-memory-min = 50M
cgroup-swap-max = 0
`
	desc, err := Parse(strings.NewReader(input), "app", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	files := make(map[string]string)
	for _, s := range desc.CgroupSettings {
		files[s.File] = s.Value
	}
	if files["memory.max"] != "1G" {
		t.Errorf("memory.max = %q", files["memory.max"])
	}
	if files["memory.high"] != "800M" {
		t.Errorf("memory.high = %q", files["memory.high"])
	}
	if files["memory.low"] != "100M" {
		t.Errorf("memory.low = %q", files["memory.low"])
	}
	if files["memory.min"] != "50M" {
		t.Errorf("memory.min = %q", files["memory.min"])
	}
	if files["memory.swap.max"] != "0" {
		t.Errorf("memory.swap.max = %q", files["memory.swap.max"])
	}
}

func TestParseCgroupCpuMax(t *testing.T) {
	// cpu.max format: "quota period" e.g., "50000 100000" for 50%
	input := `type = process
command = /bin/app
cgroup = /sys/fs/cgroup/app
cgroup-cpu-max = 50000 100000
`
	desc, err := Parse(strings.NewReader(input), "app", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(desc.CgroupSettings) != 1 {
		t.Fatalf("expected 1 setting, got %d", len(desc.CgroupSettings))
	}
	if desc.CgroupSettings[0].File != "cpu.max" || desc.CgroupSettings[0].Value != "50000 100000" {
		t.Errorf("setting = %+v", desc.CgroupSettings[0])
	}
}

func TestParseCgroupSettingBadFormat(t *testing.T) {
	input := `type = process
command = /bin/app
cgroup-setting = novalue
`
	_, err := Parse(strings.NewReader(input), "app", "test-file")
	if err == nil {
		t.Fatal("expected error for malformed cgroup-setting")
	}
}
