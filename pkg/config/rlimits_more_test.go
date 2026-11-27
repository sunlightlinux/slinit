package config

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
	"golang.org/x/sys/unix"
)

func TestMoreRlimitsParsed(t *testing.T) {
	desc, err := Parse(strings.NewReader("type = process\ncommand = /bin/true\n"+
		"rlimit-nproc = 64\nrlimit-cpu = 10:unlimited\nrlimit-nice = 25\n"), "r", "r")
	if err != nil {
		t.Fatal(err)
	}
	want := map[int][2]uint64{
		unix.RLIMIT_NPROC: {64, 64},
		unix.RLIMIT_CPU:   {10, ^uint64(0)},
		unix.RLIMIT_NICE:  {25, 25},
	}
	for r, w := range want {
		if desc.Rlimits[r] != w {
			t.Errorf("resource %d = %v, want %v", r, desc.Rlimits[r], w)
		}
	}
	for _, bad := range []string{"rlimit-nproc = many", "rlimit-stack = 9:8"} {
		if _, err := Parse(strings.NewReader("type = process\ncommand = /bin/true\n"+bad+"\n"), "r", "r"); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}

	// On the record, in resource order; a reload does not keep a limit
	// the description dropped (applyRlimits starts from nothing).
	svc := service.NewProcessService(service.NewServiceSet(&testConsumerLogger{}), "r")
	applyRlimits(svc.Record(), desc)
	show := service.RenderShow(svc)
	for _, line := range []string{"LimitNPROC=64\n", "LimitCPUSoft=10\n", "LimitNICE=25\n"} {
		if !strings.Contains(show, line) {
			t.Errorf("show lacks %q:\n%s", line, show)
		}
	}
	desc.Rlimits = nil
	applyRlimits(svc.Record(), desc)
	if strings.Contains(service.RenderShow(svc), "LimitNPROC") {
		t.Error("a dropped rlimit-nproc stayed on the record")
	}
}

func TestSystemdLimitsMapped(t *testing.T) {
	for _, c := range []struct{ unit, want string }{
		{"LimitNPROC=1", "rlimit-nproc = 1"},
		{"LimitNPROC=infinity", "rlimit-nproc = unlimited"},
		{"LimitNPROC=10:20", "rlimit-nproc = 10:20"},
		{"LimitFSIZE=1G", "rlimit-fsize = 1073741824"},
		{"LimitSTACK=8M:infinity", "rlimit-stack = 8388608:unlimited"},
		{"LimitCPU=90", "rlimit-cpu = 90"},
		{"LimitCPU=1min 30s", "rlimit-cpu = 90"},
		{"LimitCPU=1.5", "rlimit-cpu = 2"}, // rounded up, as systemd does
		{"LimitRTTIME=500", "rlimit-rttime = 500"}, // microseconds
		{"LimitRTTIME=1s", "rlimit-rttime = 1000000"},
		{"LimitNICE=-5", "rlimit-nice = 25"},
		{"LimitNICE=+5", "rlimit-nice = 15"},
		{"LimitNICE=30", "rlimit-nice = 30"},
	} {
		lines, _ := convertService(t, c.unit+"\n")
		out := strings.Join(lines, "\n")
		if !strings.Contains(out, c.want+"\n") {
			t.Errorf("%s: no %q in\n%s", c.unit, c.want, out)
		}
	}
	// An invalid value is dropped with a warning, as systemd ignores it;
	// an empty assignment unsets an earlier one.
	for _, c := range []string{"LimitNPROC=lots", "LimitNICE=+25", "LimitSTACK=9:8", "LimitNPROC=5\nLimitNPROC="} {
		lines, warns := convertService(t, c+"\n")
		out := strings.Join(lines, "\n")
		if strings.Contains(out, "rlimit-") {
			t.Errorf("%q: emitted a limit:\n%s", c, out)
		}
		if !strings.HasSuffix(c, "=") && len(warns) == 0 {
			t.Errorf("%q: dropped without a warning", c)
		}
	}
}
