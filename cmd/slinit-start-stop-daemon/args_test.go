package main

import (
	"syscall"
	"testing"
)

func TestParseArgsMinimalStart(t *testing.T) {
	opts, rest, err := parseArgs([]string{"--start", "--exec", "/bin/true"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.Mode != "start" || opts.Exec != "/bin/true" {
		t.Errorf("mode=%q exec=%q", opts.Mode, opts.Exec)
	}
	if len(rest) != 0 {
		t.Errorf("unexpected rest: %v", rest)
	}
}

func TestParseArgsStopWithSignalAndRetry(t *testing.T) {
	opts, _, err := parseArgs([]string{
		"--stop", "--pidfile", "/var/run/foo.pid",
		"--signal", "HUP", "--retry", "TERM/30/KILL/5",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.Mode != "stop" {
		t.Errorf("mode=%q", opts.Mode)
	}
	if opts.Signal != syscall.SIGHUP {
		t.Errorf("signal=%v", opts.Signal)
	}
	if opts.Retry != "TERM/30/KILL/5" {
		t.Errorf("retry=%q", opts.Retry)
	}
}

func TestParseArgsBackgroundWithMakePidfile(t *testing.T) {
	opts, rest, err := parseArgs([]string{
		"--start", "--background", "--make-pidfile",
		"--pidfile", "/var/run/foo.pid",
		"--exec", "/usr/sbin/foo",
		"--", "--config=/etc/foo.conf",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !opts.Background || !opts.MakePidfile {
		t.Errorf("flags: background=%v make-pidfile=%v", opts.Background, opts.MakePidfile)
	}
	if len(rest) != 1 || rest[0] != "--config=/etc/foo.conf" {
		t.Errorf("rest=%v", rest)
	}
}

func TestParseArgsUmaskOctal(t *testing.T) {
	opts, _, err := parseArgs([]string{"--start", "--exec", "/bin/true", "--umask", "022"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.Umask == nil || *opts.Umask != 0o022 {
		t.Errorf("umask=%v", opts.Umask)
	}
}

func TestParseArgsChuidWithGroup(t *testing.T) {
	opts, _, err := parseArgs([]string{
		"--start", "--exec", "/bin/true", "--chuid", "nobody:nogroup",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.ChUID != "nobody:nogroup" {
		t.Errorf("chuid=%q", opts.ChUID)
	}
}

func TestParseArgsIONice(t *testing.T) {
	opts, _, err := parseArgs([]string{
		"--start", "--exec", "/bin/true", "--ionice", "be:3",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.IOClass != 2 || opts.IOLevel != 3 {
		t.Errorf("ioclass=%d iolevel=%d", opts.IOClass, opts.IOLevel)
	}
}

func TestParseArgsAcceptsHardeningFlags(t *testing.T) {
	// The hardening flags are now wired through slinit-runner; they should
	// be parsed and stored, not rejected. Actual runner exec is tested
	// separately (needs the binary on PATH).
	opts, _, err := parseArgs([]string{
		"--start", "--exec", "/bin/true",
		"--capabilities", "cap_net_bind_service,cap_sys_admin",
		"--secbits", "keep_caps",
		"--no-new-privs",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.Capabilities != "cap_net_bind_service,cap_sys_admin" {
		t.Errorf("caps=%q", opts.Capabilities)
	}
	if opts.Securebits != "keep_caps" {
		t.Errorf("secbits=%q", opts.Securebits)
	}
	if !opts.NoNewPrivs {
		t.Errorf("nnp=false, want true")
	}
}

func TestParseArgsScheduler(t *testing.T) {
	opts, _, err := parseArgs([]string{
		"--start", "--exec", "/bin/true",
		"--scheduler", "fifo", "--scheduler-priority", "10",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.Scheduler != "fifo" || opts.SchedulerPriority != 10 {
		t.Errorf("sched=%q prio=%d", opts.Scheduler, opts.SchedulerPriority)
	}
}

func TestParseArgsLoggerAndNotify(t *testing.T) {
	opts, _, err := parseArgs([]string{
		"--start", "--exec", "/bin/true",
		"--stdout-logger", "logger -p daemon.info",
		"--stderr-logger", "logger -p daemon.err",
		"--notify", "readiness=pidfile",
		"--interpreted", "--progress",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.StdoutLogger != "logger -p daemon.info" {
		t.Errorf("stdout-logger=%q", opts.StdoutLogger)
	}
	if opts.StderrLogger != "logger -p daemon.err" {
		t.Errorf("stderr-logger=%q", opts.StderrLogger)
	}
	if opts.Notify != "readiness=pidfile" {
		t.Errorf("notify=%q", opts.Notify)
	}
	if !opts.Interpreted || !opts.Progress {
		t.Errorf("interpreted=%v progress=%v", opts.Interpreted, opts.Progress)
	}
}

func TestParseArgsFusedAndSpaced(t *testing.T) {
	// --exec=path is equivalent to --exec path
	opts1, _, err := parseArgs([]string{"--start", "--exec=/bin/true"})
	if err != nil {
		t.Fatalf("fused: %v", err)
	}
	opts2, _, err := parseArgs([]string{"--start", "--exec", "/bin/true"})
	if err != nil {
		t.Fatalf("spaced: %v", err)
	}
	if opts1.Exec != opts2.Exec {
		t.Errorf("fused=%q spaced=%q", opts1.Exec, opts2.Exec)
	}
}

func TestParseArgsShortFused(t *testing.T) {
	// -x/bin/true (attached) same as -x /bin/true
	opts, _, err := parseArgs([]string{"-S", "-x/bin/true"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.Exec != "/bin/true" {
		t.Errorf("exec=%q", opts.Exec)
	}
	if opts.Mode != "start" {
		t.Errorf("mode=%q", opts.Mode)
	}
}
