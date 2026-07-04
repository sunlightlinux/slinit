package main

import (
	"syscall"
	"testing"
	"time"
)

func TestParseArgsMinimalStart(t *testing.T) {
	opts, err := parseArgs([]string{"foo", "--start", "--exec", "/bin/true", "--pidfile", "/run/foo.pid"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.Mode != "start" || opts.Service != "foo" || opts.Exec != "/bin/true" || opts.PidFile != "/run/foo.pid" {
		t.Errorf("mode=%q svc=%q exec=%q pid=%q",
			opts.Mode, opts.Service, opts.Exec, opts.PidFile)
	}
}

func TestParseArgsDefaultModeIsStart(t *testing.T) {
	// Per the manpage: absent --stop / --signal, we assume --start.
	opts, err := parseArgs([]string{"foo", "--exec", "/bin/true", "--pidfile", "/x"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.Mode != "start" {
		t.Errorf("mode=%q, want start", opts.Mode)
	}
}

func TestParseArgsSignalImpliesMode(t *testing.T) {
	opts, err := parseArgs([]string{"foo", "--signal", "HUP", "--pidfile", "/run/foo.pid"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.Mode != "signal" {
		t.Errorf("mode=%q, want signal", opts.Mode)
	}
	if opts.Signal != syscall.SIGHUP {
		t.Errorf("signal=%v, want SIGHUP", opts.Signal)
	}
}

func TestParseArgsPositionalAsService(t *testing.T) {
	opts, err := parseArgs([]string{
		"nginx", "--start", "--exec", "/usr/sbin/nginx",
		"--pidfile", "/run/nginx.pid",
		"--", "-g", "daemon off;",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.Service != "nginx" {
		t.Errorf("service=%q", opts.Service)
	}
	if len(opts.Args) != 2 || opts.Args[0] != "-g" || opts.Args[1] != "daemon off;" {
		t.Errorf("args=%v", opts.Args)
	}
}

func TestParseArgsUserWithGroupShorthand(t *testing.T) {
	opts, err := parseArgs([]string{
		"foo", "--start", "--exec", "/bin/true",
		"--pidfile", "/x", "--user", "nobody:nogroup",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.User != "nobody" || opts.Group != "nogroup" {
		t.Errorf("user=%q group=%q", opts.User, opts.Group)
	}
}

func TestParseArgsRespawnPolicyDefaults(t *testing.T) {
	opts, err := parseArgs([]string{"foo", "--start", "--exec", "/bin/true", "--pidfile", "/x"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.RespawnMax != defaultRespawnMax ||
		opts.RespawnPeriod != defaultRespawnPeriod ||
		opts.RespawnDelayStep != defaultDelayStep ||
		opts.RespawnDelayCap != defaultDelayCap {
		t.Errorf("defaults not applied: max=%d period=%v step=%v cap=%v",
			opts.RespawnMax, opts.RespawnPeriod,
			opts.RespawnDelayStep, opts.RespawnDelayCap)
	}
}

func TestParseArgsRespawnOverrides(t *testing.T) {
	opts, err := parseArgs([]string{
		"foo", "--start", "--exec", "/bin/true", "--pidfile", "/x",
		"--respawn-delay", "1000ms",
		"--respawn-period", "60",
		"--respawn-max", "5",
		"--respawn-delay-step", "200ms",
		"--respawn-delay-cap", "10sec",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.RespawnDelay != time.Second {
		t.Errorf("delay=%v", opts.RespawnDelay)
	}
	if opts.RespawnPeriod != time.Minute {
		t.Errorf("period=%v", opts.RespawnPeriod)
	}
	if opts.RespawnMax != 5 {
		t.Errorf("max=%d", opts.RespawnMax)
	}
	if opts.RespawnDelayStep != 200*time.Millisecond {
		t.Errorf("step=%v", opts.RespawnDelayStep)
	}
	if opts.RespawnDelayCap != 10*time.Second {
		t.Errorf("cap=%v", opts.RespawnDelayCap)
	}
}

func TestParseArgsHardeningFlags(t *testing.T) {
	opts, err := parseArgs([]string{
		"foo", "--start", "--exec", "/bin/true", "--pidfile", "/x",
		"--capabilities", "cap_net_bind_service",
		"--secbits", "keep_caps",
		"--no-new-privs",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.Capabilities != "cap_net_bind_service" {
		t.Errorf("caps=%q", opts.Capabilities)
	}
	if opts.Securebits != "keep_caps" {
		t.Errorf("secbits=%q", opts.Securebits)
	}
	if !opts.NoNewPrivs {
		t.Errorf("nnp=false")
	}
}

func TestParseArgsShortFlagsFused(t *testing.T) {
	opts, err := parseArgs([]string{"foo", "-S", "-x/bin/true", "-p/run/foo.pid", "-N10"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.Mode != "start" || opts.Exec != "/bin/true" || opts.PidFile != "/run/foo.pid" {
		t.Errorf("mode=%q exec=%q pid=%q", opts.Mode, opts.Exec, opts.PidFile)
	}
	if opts.Nice == nil || *opts.Nice != 10 {
		t.Errorf("nice=%v", opts.Nice)
	}
}

func TestParseDurationOpenRCStyle(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"5", 5 * time.Second},
		{"500ms", 500 * time.Millisecond},
		{"30sec", 30 * time.Second},
		{"2min", 2 * time.Minute},
		{"1hour", time.Hour},
		{"128ms", 128 * time.Millisecond},
	}
	for _, tc := range cases {
		got, err := parseDuration(tc.in)
		if err != nil {
			t.Errorf("parseDuration(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseDuration(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	// Malformed shouldn't parse.
	if _, err := parseDuration("nope"); err == nil {
		t.Errorf("parseDuration(nope): expected error")
	}
}

func TestParseArgsRejectsUnknown(t *testing.T) {
	if _, err := parseArgs([]string{"foo", "--gibberish"}); err == nil {
		t.Errorf("expected error for --gibberish")
	}
}
