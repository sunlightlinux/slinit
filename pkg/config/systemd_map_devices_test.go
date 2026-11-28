package config

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestSystemdDeviceFilter(t *testing.T) {
	_, warns, desc := lcConvert(t, "DevicePolicy=closed\nDeviceAllow=/dev/fuse rw\nDeviceAllow=char-pts rw\nDeviceAllow=block-loop")
	if desc.DevicePolicy != "closed" || strings.Join(desc.DeviceAllow, "|") != "/dev/fuse rw|char-pts rw|block-loop" {
		t.Errorf("policy %q allow %q", desc.DevicePolicy, desc.DeviceAllow)
	}
	// A cgroup of its own, as systemd gives every service.
	if desc.Slice != "system.slice" || !lcWarned(warns, "NOTE", "slice = system.slice") {
		t.Errorf("slice %q warns %v", desc.Slice, warns)
	}
	// Slice= kept; an empty DeviceAllow= clears; auto is the default.
	_, _, desc = lcConvert(t, "Slice=foo.slice\nDevicePolicy=auto\nDeviceAllow=/dev/a\nDeviceAllow=\nDeviceAllow=/dev/b r")
	if desc.DevicePolicy != "" || strings.Join(desc.DeviceAllow, "|") != "/dev/b r" || desc.Slice != "foo.slice" {
		t.Errorf("policy %q allow %q slice %q", desc.DevicePolicy, desc.DeviceAllow, desc.Slice)
	}
	// A value slinit cannot take refuses the unit: it confines.
	for _, bad := range []string{"DeviceAllow=/dev/sda rx", "DevicePolicy=open", "DeviceAllow=sda"} {
		if _, err := lcConvertErr(t, bad); err == nil {
			t.Errorf("%s converted", bad)
		}
	}
}

// lcConvertErr loads a unit with the [Service] lines directly and
// returns the loader's error.
func lcConvertErr(t *testing.T, lines string) (*ServiceDescription, error) {
	t.Helper()
	p := writeUnit(t, t.TempDir(), "lc.service", "[Service]\nExecStart=/usr/bin/lc\n"+lines+"\n")
	return SystemdUnitToServiceDescription(p, "lc")
}

// Natively: device-allow lines add up, a bad one is a parse error, and
// the filter without a cgroup of the service's own is refused, as on
// the daemon's shared cgroup it would confine every service there.
func TestDeviceFilterSettings(t *testing.T) {
	desc, err := Parse(strings.NewReader("type = process\ncommand = /bin/true\ndevice-policy = strict\n"+
		"device-allow = /dev/null rw\ndevice-allow += char-pts\n"), "x", "x")
	if err != nil || desc.DevicePolicy != "strict" || strings.Join(desc.DeviceAllow, "|") != "/dev/null rw|char-pts" {
		t.Fatalf("%v %q %q", err, desc.DevicePolicy, desc.DeviceAllow)
	}
	for _, bad := range []string{"device-policy = open", "device-allow = /dev/null x", "device-allow = null"} {
		if _, err := Parse(strings.NewReader("type = process\ncommand = /bin/true\n"+bad+"\n"), "x", "x"); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	for _, c := range []struct {
		extra string
		ok    bool
	}{{"", false}, {"slice = system.slice\n", true}, {"cgroup = /sys/fs/cgroup/x\n", true}} {
		dir := t.TempDir()
		writeNSServiceFile(t, dir, "d", "type = process\ncommand = /bin/true\ndevice-policy = closed\n"+c.extra)
		ss := service.NewServiceSet(&testServiceLogger{})
		loader := NewDirLoader(ss, []string{dir})
		ss.SetLoader(loader)
		_, err := loader.LoadService("d")
		if (err == nil) != c.ok || (err != nil && !strings.Contains(err.Error(), "cgroup of the service's own")) {
			t.Errorf("with %q: err %v", c.extra, err)
		}
	}
}
