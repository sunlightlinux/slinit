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
	// Slice= kept; an empty DeviceAllow= clears; auto is carried.
	_, _, desc = lcConvert(t, "Slice=foo.slice\nDevicePolicy=auto\nDeviceAllow=/dev/a\nDeviceAllow=\nDeviceAllow=/dev/b r")
	if desc.DevicePolicy != "auto" || strings.Join(desc.DeviceAllow, "|") != "/dev/b r" || desc.Slice != "foo.slice" {
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

func TestSystemdIPFilter(t *testing.T) {
	_, warns, desc := lcConvert(t, "IPAddressDeny=any\nIPAddressAllow=localhost\nIPAddressAllow=10.0.0.0/8 ::1")
	if strings.Join(desc.IPAddressDeny, "|") != "any" || strings.Join(desc.IPAddressAllow, "|") != "localhost|10.0.0.0/8 ::1" {
		t.Errorf("deny %q allow %q", desc.IPAddressDeny, desc.IPAddressAllow)
	}
	if desc.Slice != "system.slice" || !lcWarned(warns, "NOTE", "slice = system.slice") {
		t.Errorf("slice %q", desc.Slice)
	}
	_, _, desc = lcConvert(t, "IPAddressDeny=any\nIPAddressDeny=")
	if len(desc.IPAddressDeny) != 0 {
		t.Errorf("an empty IPAddressDeny= left %q", desc.IPAddressDeny)
	}
	if _, err := lcConvertErr(t, "IPAddressDeny=10.0.0.0/33"); err == nil {
		t.Error("a bad prefix converted")
	}
	// Natively, and refused without a cgroup of its own.
	d, err := Parse(strings.NewReader("type = process\ncommand = /bin/true\nip-address-deny = any\nip-address-allow += localhost\n"), "x", "x")
	if err != nil || strings.Join(d.IPAddressDeny, "|") != "any" || strings.Join(d.IPAddressAllow, "|") != "localhost" {
		t.Fatalf("%v %q %q", err, d.IPAddressDeny, d.IPAddressAllow)
	}
	dir := t.TempDir()
	writeNSServiceFile(t, dir, "i", "type = process\ncommand = /bin/true\nip-address-deny = any\n")
	ss := service.NewServiceSet(&testServiceLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)
	if _, err := loader.LoadService("i"); err == nil || !strings.Contains(err.Error(), "cgroup of the service's own") {
		t.Errorf("no own cgroup: %v", err)
	}
}

// Review fixes: a later DevicePolicy=auto relaxes an earlier strict; an
// empty one is ignored, not a refusal; auto alone makes no filter and
// needs no cgroup; a filter on the cgroup root or a slice is refused.
func TestDeviceFilterEdges(t *testing.T) {
	_, _, desc := lcConvert(t, "DevicePolicy=strict\nDevicePolicy=auto")
	if desc.DevicePolicy != "auto" || desc.Slice != "" {
		t.Errorf("strict then auto: policy %q slice %q", desc.DevicePolicy, desc.Slice)
	}
	_, _, desc = lcConvert(t, "DevicePolicy=closed\nDevicePolicy=")
	if desc.DevicePolicy != "closed" {
		t.Errorf("an empty DevicePolicy= changed it to %q", desc.DevicePolicy)
	}
	for _, c := range []struct {
		body string
		ok   bool
	}{
		{"device-policy = auto\n", true},
		{"device-policy = closed\ncgroup = /sys/fs/cgroup\n", false},
		{"device-policy = closed\ncgroup = /sys/fs/cgroup/system.slice\n", false},
		{"ip-address-deny = any\ncgroup = /sys/fs/cgroup/\n", false},
		{"ip-address-deny = any\ncgroup = /sys/fs/cgroup/svc-x\n", true},
	} {
		dir := t.TempDir()
		writeNSServiceFile(t, dir, "e", "type = process\ncommand = /bin/true\n"+c.body)
		ss := service.NewServiceSet(&testServiceLogger{})
		loader := NewDirLoader(ss, []string{dir})
		ss.SetLoader(loader)
		if _, err := loader.LoadService("e"); (err == nil) != c.ok {
			t.Errorf("%q: err %v", c.body, err)
		}
	}
}

// IP filter review fixes: systemd's localhost spellings, a bad word of
// IPAddressAllow= skipped (one of IPAddressDeny= still refuses), and a
// filter on a cgroup holding another service refused.
func TestIPFilterEdges(t *testing.T) {
	_, warns, desc := lcConvert(t, "IPAddressDeny=any\nIPAddressAllow=localhost.localdomain 10.0.0.300 ::1")
	if strings.Join(desc.IPAddressAllow, "|") != "localhost.localdomain ::1" || !lcWarned(warns, "WARN", "skipped") {
		t.Errorf("allow %q warns %v", desc.IPAddressAllow, warns)
	}
	if _, err := lcConvertErr(t, "IPAddressDeny=10.0.0.300"); err == nil {
		t.Error("a bad IPAddressDeny= word converted")
	}
	dir := t.TempDir()
	writeNSServiceFile(t, dir, "a", "type = process\ncommand = /bin/true\nslice = apps\n")
	writeNSServiceFile(t, dir, "b", "type = process\ncommand = /bin/true\ncgroup = /sys/fs/cgroup/apps\nip-address-deny = any\n")
	ss := service.NewServiceSet(&testServiceLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)
	if _, err := loader.LoadService("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := loader.LoadService("b"); err == nil || !strings.Contains(err.Error(), "would confine service 'a'") {
		t.Errorf("b over a's slice: %v", err)
	}
	ss.SetDefaultCgroupPath("/sys/fs/cgroup/slinit")
	writeNSServiceFile(t, dir, "c", "type = process\ncommand = /bin/true\ncgroup = /sys/fs/cgroup/slinit\ndevice-allow = /dev/null\n")
	if _, err := loader.LoadService("c"); err == nil || !strings.Contains(err.Error(), "shared default") {
		t.Errorf("c on the default cgroup: %v", err)
	}
}

func TestSystemdRequiresMountsFor(t *testing.T) {
	writeUnitBody := "[Unit]\nRequiresMountsFor=/var/lib/foo /srv/x\nRequiresMountsFor=/var/lib/foo\n[Service]\nExecStart=/usr/bin/lc\n"
	p := writeUnit(t, t.TempDir(), "lc.service", writeUnitBody)
	desc, err := SystemdUnitToServiceDescription(p, "lc")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, pr := range desc.Predicates {
		if pr.IsAssert && pr.Kind == service.PredMountsFor {
			got = append(got, pr.Param)
		}
	}
	if strings.Join(got, "|") != "/var/lib/foo|/srv/x" {
		t.Errorf("assert-mounts-for %q", got)
	}
	p = writeUnit(t, t.TempDir(), "lc.service", "[Unit]\nRequiresMountsFor=/a\nRequiresMountsFor=\n[Service]\nExecStart=/usr/bin/lc\n")
	desc, _ = SystemdUnitToServiceDescription(p, "lc")
	if len(desc.Predicates) != 0 {
		t.Errorf("an empty RequiresMountsFor= left %v", desc.Predicates)
	}
}
