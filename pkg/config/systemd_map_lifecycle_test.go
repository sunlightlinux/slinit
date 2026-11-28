package config

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// lcConvert runs a unit holding the [Service] lines through both
// consumers: the converter (ConvertSystemdUnit + EmitSlinitFile, the
// emitted lines) and the direct loader (SystemdUnitToServiceDescription,
// which parses what was emitted).
func lcConvert(t *testing.T, lines string) (emitted []string, warns []SystemdWarning, desc *ServiceDescription) {
	t.Helper()
	p := writeUnit(t, t.TempDir(), "lc.service", "[Service]\nExecStart=/usr/bin/lc\n"+lines+"\n")
	cfg, warns, err := ConvertSystemdUnit(p)
	if err != nil {
		t.Fatalf("convert:\n%s\n%v", lines, err)
	}
	var buf bytes.Buffer
	EmitSlinitFile(&buf, cfg)
	desc, err = SystemdUnitToServiceDescription(p, "lc")
	if err != nil {
		t.Fatalf("load:\n%s\n%v", lines, err)
	}
	for _, l := range strings.Split(buf.String(), "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			emitted = append(emitted, l)
		}
	}
	return emitted, warns, desc
}

// lcWarned reports whether a warning of level holds every one of subs.
func lcWarned(warns []SystemdWarning, level string, subs ...string) bool {
	for _, w := range warns {
		if w.Level != level {
			continue
		}
		ok := true
		for _, s := range subs {
			ok = ok && strings.Contains(w.Msg, s)
		}
		if ok {
			return true
		}
	}
	return false
}

func lcEq[T comparable](what string, got, want T) error {
	if got != want {
		return fmt.Errorf("%s = %v, want %v", what, got, want)
	}
	return nil
}

func TestSystemdLifecycleMappings(t *testing.T) {
	for _, tc := range []struct {
		name  string
		unit  string
		want  []string // emitted lines, exactly
		none  []string // slinit keys that must not be emitted
		warn  []string // level, then substrings of one warning
		check func(d *ServiceDescription) error
	}{
		// Exit status lists.
		{name: "prevent", unit: "RestartPreventExitStatus=1 SIGUSR1 TEMPFAIL",
			want: []string{"normal-exit = 1 SIGUSR1 75"},
			check: func(d *ServiceDescription) error {
				if !slices.Equal(d.NormalExitCodes, []int{1, 75}) || !slices.Equal(d.NormalExitSignals, []syscall.Signal{syscall.SIGUSR1}) {
					return fmt.Errorf("normal-exit = %v %v", d.NormalExitCodes, d.NormalExitSignals)
				}
				return nil
			}},
		{name: "prevent keeps 0", unit: "RestartPreventExitStatus=0", want: []string{"normal-exit = 0"}},
		{name: "success", unit: "SuccessExitStatus=0 143 INVALIDARGUMENT TERM",
			want: []string{"normal-exit = 143 2 SIGTERM"}, warn: []string{"NOTE", "SuccessExitStatus=", "restart = yes"}},
		{name: "success 0 only", unit: "SuccessExitStatus=SUCCESS", none: []string{"normal-exit"}},
		{name: "success reset", unit: "SuccessExitStatus=1\nSuccessExitStatus=\nSuccessExitStatus=2",
			want: []string{"normal-exit = 2"}},
		{name: "success and prevent accumulate", unit: "SuccessExitStatus=1\nRestartPreventExitStatus=5",
			want: []string{"normal-exit = 5 1"}},
		{name: "bad words dropped, rest kept", unit: "SuccessExitStatus=3 BOGUS 300 sigterm",
			want: []string{"normal-exit = 3"}, warn: []string{"WARN", "BOGUS", "left out"}},
		{name: "realtime signal", unit: "SuccessExitStatus=RTMIN+2",
			none: []string{"normal-exit"}, warn: []string{"WARN", "realtime"}},
		{name: "force", unit: "RestartForceExitStatus=7 SIGHUP",
			want: []string{"restart-force-exit-status = 7"}, warn: []string{"WARN", "SIGHUP", "exit codes only"},
			check: func(d *ServiceDescription) error {
				if !slices.Equal(d.RestartForceExitCodes, []int{7}) {
					return fmt.Errorf("restart-force-exit-status = %v", d.RestartForceExitCodes)
				}
				return nil
			}},

		// Killing.
		{name: "kill-mode", unit: "KillMode=control-group", want: []string{"kill-mode = control-group"},
			check: func(d *ServiceDescription) error { return lcEq("KillMode", d.KillMode, service.KillModeControlGroup) }},
		{name: "kill-mode process", unit: "KillMode=process", want: []string{"kill-mode = process"},
			warn: []string{"NOTE", "process group"}},
		{name: "kill-mode none", unit: "KillMode=none", want: []string{"kill-mode = none"},
			warn: []string{"NOTE", "final-kill-signal"}},
		{name: "kill-mode bad", unit: "KillMode=all", none: []string{"kill-mode"}, warn: []string{"WARN", "KillMode=all ignored"}},
		{name: "final signal", unit: "FinalKillSignal=QUIT", want: []string{"final-kill-signal = SIGQUIT"},
			check: func(d *ServiceDescription) error { return lcEq("FinalKillSignal", d.FinalKillSignal, syscall.SIGQUIT) }},
		{name: "signal by number", unit: "FinalKillSignal=9", want: []string{"final-kill-signal = 9"}},
		{name: "realtime by name", unit: "RestartKillSignal=SIGRTMIN+1", want: []string{"restart-kill-signal = 35"},
			check: func(d *ServiceDescription) error {
				return lcEq("RestartKillSignal", d.RestartKillSignal, syscall.Signal(35))
			}},
		{name: "rtmax", unit: "RestartKillSignal=RTMAX-4", want: []string{"restart-kill-signal = 60"}},
		// systemd's names are case-sensitive; slinit's are not, so the
		// conv must refuse what systemd refuses.
		{name: "signal case", unit: "WatchdogSignal=sigusr1", none: []string{"watchdog-signal"}, warn: []string{"WARN", "WatchdogSignal"}},
		{name: "signal 0", unit: "WatchdogSignal=0", none: []string{"watchdog-signal"}, warn: []string{"WARN", "WatchdogSignal"}},
		{name: "watchdog signal", unit: "WatchdogSignal=SIGUSR2", want: []string{"watchdog-signal = SIGUSR2"},
			check: func(d *ServiceDescription) error { return lcEq("WatchdogSignal", d.WatchdogSignal, syscall.SIGUSR2) }},
		{name: "reload signal", unit: "ReloadSignal=SIGHUP\nReloadSignal=USR1", want: []string{"reload-signal = SIGUSR1"},
			check: func(d *ServiceDescription) error { return lcEq("ReloadSignal", d.ReloadSignal, syscall.SIGUSR1) }},
		{name: "no SIGKILL", unit: "SendSIGKILL=off", want: []string{"survive-final-kill-signal = yes"},
			warn: []string{"NOTE", "SendSIGKILL"},
			check: func(d *ServiceDescription) error {
				return lcEq("SurviveFinalKillSignal", d.SurviveFinalKillSignal, true)
			}},
		{name: "SIGKILL", unit: "SendSIGKILL=yes", want: []string{"survive-final-kill-signal = no"}},
		{name: "stop failure abort", unit: "TimeoutStopFailureMode=abort", want: []string{"timeout-stop-failure-mode = abort"},
			warn: []string{"NOTE", "timeout-abort-sec"},
			check: func(d *ServiceDescription) error {
				return lcEq("mode", d.TimeoutStopFailureMode, service.TimeoutFailureAbort)
			}},
		{name: "start failure kill", unit: "TimeoutStartFailureMode=kill", want: []string{"timeout-start-failure-mode = kill"},
			check: func(d *ServiceDescription) error {
				return lcEq("mode", d.TimeoutStartFailureMode, service.TimeoutFailureKill)
			}},
		{name: "exit type", unit: "ExitType=cgroup", want: []string{"exit-type = cgroup"},
			check: func(d *ServiceDescription) error { return lcEq("ExitType", d.ExitType, service.ExitTypeCgroup) }},
		{name: "oom", unit: "OOMPolicy=kill", want: []string{"oom-policy = kill"},
			check: func(d *ServiceDescription) error { return lcEq("OOMPolicy", d.OOMPolicy, service.OOMKill) }},
		{name: "oom case", unit: "OOMPolicy=Kill", none: []string{"oom-policy"}, warn: []string{"WARN", "OOMPolicy=Kill ignored"}},

		// Restart and rate limit.
		{name: "restart mode", unit: "RestartMode=direct", want: []string{"restart-mode = direct"},
			check: func(d *ServiceDescription) error {
				return lcEq("RestartMode", d.RestartMode, service.RestartModeDirect)
			}},
		{name: "restart mode debug", unit: "RestartMode=debug", want: []string{"restart-mode = normal"}, warn: []string{"NOTE", "debug"}},
		{name: "restart jitter", unit: "RestartRandomizedDelaySec=1min 500ms", want: []string{"restart-randomized-delay = 60.5"},
			check: func(d *ServiceDescription) error {
				return lcEq("RestartRandomizedDelay", d.RestartRandomizedDelay, 60500*time.Millisecond)
			}},
		{name: "restart jitter infinity", unit: "RestartRandomizedDelaySec=infinity", none: []string{"restart-randomized-delay"},
			warn: []string{"WARN", "RestartRandomizedDelaySec=infinity ignored"}},
		{name: "start limit", unit: "StartLimitBurst=5\nStartLimitInterval=1min 30s",
			want: []string{"restart-limit-count = 5", "restart-limit-interval = 90"}, warn: []string{"NOTE", "every start"},
			check: func(d *ServiceDescription) error {
				if err := lcEq("RestartLimitCount", d.RestartLimitCount, 5); err != nil {
					return err
				}
				return lcEq("RestartInterval", d.RestartInterval, 90*time.Second)
			}},
		{name: "start limit off", unit: "StartLimitBurst=0", want: []string{"restart-limit-count = 0"}},
		{name: "start limit forever", unit: "StartLimitInterval=infinity", want: []string{"restart-limit-interval = infinity"},
			check: func(d *ServiceDescription) error {
				return lcEq("RestartInterval", d.RestartInterval, maxConfigDuration)
			}},
		{name: "start limit bad", unit: "StartLimitBurst=-1", none: []string{"restart-limit-count"}, warn: []string{"WARN", "StartLimitBurst"}},

		// Actions.
		{name: "failure action", unit: "FailureAction=poweroff", want: []string{"failure-action = poweroff"},
			check: func(d *ServiceDescription) error {
				return lcEq("FailureAction", d.FailureAction, service.ActionPoweroff)
			}},
		{name: "forced action", unit: "FailureAction=reboot-force", want: []string{"failure-action = reboot"}, warn: []string{"NOTE", "reboot-force"}},
		{name: "kexec", unit: "StartLimitAction=kexec", want: []string{"start-limit-action = reboot"}, warn: []string{"NOTE", "kexec"},
			check: func(d *ServiceDescription) error {
				return lcEq("StartLimitAction", d.StartLimitAction, service.ActionReboot)
			}},
		{name: "exit-force", unit: "StartLimitAction=exit-force", want: []string{"start-limit-action = exit"}},
		{name: "action bad", unit: "FailureAction=", none: []string{"failure-action"}, warn: []string{"WARN", "FailureAction"}},
		{name: "reboot argument", unit: "RebootArgument=a$b %n", want: []string{"reboot-argument = a$$b lc.service"},
			warn:  []string{"NOTE", "reboot(2)"},
			check: func(d *ServiceDescription) error { return lcEq("RebootArgument", d.RebootArgument, "a$b lc.service") }},

		// Runtime limit.
		{name: "runtime max", unit: "RuntimeMaxSec=1d", want: []string{"runtime-max-sec = 86400s"}, warn: []string{"NOTE", "timed out", "restart= applies"},
			check: func(d *ServiceDescription) error { return lcEq("RuntimeMaxSec", d.RuntimeMaxSec, 24*time.Hour) }},
		{name: "runtime max fraction", unit: "RuntimeMaxSec=500ms", want: []string{"runtime-max-sec = 0.5s"}},
		{name: "runtime max reset", unit: "RuntimeMaxSec=10\nRuntimeMaxSec=infinity", none: []string{"runtime-max-sec"},
			check: func(d *ServiceDescription) error { return lcEq("RuntimeMaxSec", d.RuntimeMaxSec, time.Duration(0)) }},
		{name: "runtime max 0", unit: "RuntimeMaxSec=0", none: []string{"runtime-max-sec"}, warn: []string{"WARN", "RuntimeMaxSec=0 ignored"}},
		{name: "runtime max huge", unit: "RuntimeMaxSec=500y", none: []string{"runtime-max-sec"}},
		{name: "runtime extra", unit: "RuntimeRandomizedExtraSec=5min", want: []string{"runtime-randomized-extra = 300"},
			check: func(d *ServiceDescription) error {
				return lcEq("RuntimeRandomizedExtra", d.RuntimeRandomizedExtra, 5*time.Minute)
			}},

		// Process setup.
		{name: "sigpipe", unit: "IgnoreSIGPIPE=off", want: []string{"ignore-sigpipe = no"},
			check: func(d *ServiceDescription) error {
				if d.IgnoreSIGPIPE == nil || *d.IgnoreSIGPIPE {
					return fmt.Errorf("IgnoreSIGPIPE = %v, want explicit no", d.IgnoreSIGPIPE)
				}
				return nil
			}},
		{name: "sigpipe bad", unit: "IgnoreSIGPIPE=maybe", none: []string{"ignore-sigpipe"}, warn: []string{"WARN", "IgnoreSIGPIPE=maybe"}},
		{name: "guess", unit: "GuessMainPID=y", want: []string{"guess-main-pid = yes"},
			check: func(d *ServiceDescription) error { return lcEq("GuessMainPID", d.GuessMainPID, true) }},
		{name: "notify access", unit: "NotifyAccess=all", want: []string{"notify-access = all"},
			check: func(d *ServiceDescription) error {
				return lcEq("NotifyAccess", d.NotifyAccess, service.NotifyAccessAll)
			}},
		{name: "bus name", unit: "Type=dbus\nBusName=org.example.%N", want: []string{"bus-name = org.example.lc"}, warn: []string{"NOTE", "system bus"},
			check: func(d *ServiceDescription) error { return lcEq("BusName", d.BusName, "org.example.lc") }},
		{name: "bus name empty", unit: "BusName=org.example.A\nBusName=\nType=dbus", want: []string{"bus-name = org.example.A"},
			warn: []string{"WARN", "BusName="}},
		{name: "fd store", unit: "FileDescriptorStoreMax=16\nFileDescriptorStorePreserve=restart",
			want: []string{"file-descriptor-store-max = 16", "file-descriptor-store-preserve = no"},
			check: func(d *ServiceDescription) error {
				if err := lcEq("FileDescriptorStoreMax", d.FileDescriptorStoreMax, 16); err != nil {
					return err
				}
				return lcEq("FileDescriptorStorePreserve", d.FileDescriptorStorePreserve, "no")
			}},
		{name: "fd store yes", unit: "FileDescriptorStorePreserve=on", want: []string{"file-descriptor-store-preserve = yes"}},
		{name: "fd store no", unit: "FileDescriptorStorePreserve=no", want: []string{"file-descriptor-store-preserve = no"},
			warn: []string{"NOTE", "every stop"}},
		{name: "fd store on-success", unit: "FileDescriptorStorePreserve=on-success", want: []string{"file-descriptor-store-preserve = on-success"}},
		{name: "fd store bad", unit: "FileDescriptorStoreMax=lots", none: []string{"file-descriptor-store-max"}, warn: []string{"WARN", "FileDescriptorStoreMax"}},

		// ExecCondition.
		{name: "condition", unit: "ExecCondition=/usr/bin/test -f \"/etc/a b\"",
			want: []string{`exec-condition = '/usr/bin/test' '-f' '/etc/a b'`}, warn: []string{"NOTE", "exit 255"},
			check: func(d *ServiceDescription) error {
				if len(d.Predicates) != 1 || d.Predicates[0].Kind != service.PredExecCondition ||
					d.Predicates[0].Param != `'/usr/bin/test' '-f' '/etc/a b'` {
					return fmt.Errorf("predicates = %+v", d.Predicates)
				}
				return nil
			}},
		{name: "conditions accumulate", unit: "ExecCondition=/bin/a\nExecCondition=+/bin/b ; /bin/c \"it's\"",
			want: []string{"exec-condition = '/bin/a'", "exec-condition = +'/bin/b'", `exec-condition = '/bin/c' 'it'\''s'`},
			check: func(d *ServiceDescription) error {
				if len(d.Predicates) != 3 || !d.Predicates[1].Privileged || d.Predicates[0].Privileged {
					return fmt.Errorf("predicates = %+v", d.Predicates)
				}
				return nil
			}},
		{name: "condition reset", unit: "ExecCondition=/bin/a\nExecCondition=\nExecCondition=/bin/b",
			want:  []string{"exec-condition = '/bin/b'"},
			check: func(d *ServiceDescription) error { return lcEq("predicates", len(d.Predicates), 1) }},
		{name: "condition dash", unit: "ExecCondition=-/bin/false",
			want: []string{`exec-condition = '/bin/sh' '-c' '"$@" || :' 'sh' '/bin/false'`}},
		{name: "condition dollar", unit: "ExecCondition=/bin/echo $$x",
			want:  []string{"exec-condition = '/bin/echo' '$$x'"},
			check: func(d *ServiceDescription) error { return lcEq("param", d.Predicates[0].Param, "'/bin/echo' '$x'") }},

		// RemainAfterExit= and WatchdogSec=.
		{name: "remain", unit: "RemainAfterExit=yes", warn: []string{"NOTE", "Type=oneshot"}},
		{name: "remain bad", unit: "RemainAfterExit=perhaps", warn: []string{"WARN", "RemainAfterExit=perhaps ignored"}},
		{name: "watchdog", unit: "WatchdogSec=30s", none: []string{"watchdog-timeout"}, warn: []string{"NOTE", "WATCHDOG=1"}},

		// Gaps.
		{name: "abort sec", unit: "TimeoutAbortSec=30", none: []string{"timeout-abort-sec"},
			warn: []string{"NOTE", "TimeoutAbortSec=30 has no slinit equivalent"}},
		{name: "steps", unit: "RestartSteps=5\nRestartMaxDelaySec=1min",
			want: []string{"restart-steps = 5", "restart-delay-cap = 60", "restart-delay = 0.1"},
			none: []string{"restart-max-delay", "restart-delay-step"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			emitted, warns, desc := lcConvert(t, tc.unit)
			for _, w := range tc.want {
				if !slices.Contains(emitted, w) {
					t.Errorf("%q not emitted:\n%s", w, strings.Join(emitted, "\n"))
				}
			}
			for _, k := range tc.none {
				for _, l := range emitted {
					if strings.HasPrefix(l, k+" =") {
						t.Errorf("%q emitted", l)
					}
				}
			}
			if len(tc.warn) > 0 && !lcWarned(warns, tc.warn[0], tc.warn[1:]...) {
				t.Errorf("no %s with %q among %+v", tc.warn[0], tc.warn[1:], warns)
			}
			if tc.check != nil {
				if err := tc.check(desc); err != nil {
					t.Error(err)
				}
			}
		})
	}
}

// A value that only restates systemd's default, or turns the directive
// off, needs no word.
func TestSystemdLifecycleQuietDefaults(t *testing.T) {
	for _, unit := range []string{
		"RemainAfterExit=no", "WatchdogSec=0", "WatchdogSec=infinity", "SendSIGHUP=no",
		"PermissionsStartOnly=false", "RestartSteps=0", "KillMode=control-group", "BusPolicy=talk org.x",
		"SysVStartPriority=5", "TimeoutCleanSec=10",
	} {
		_, warns, _ := lcConvert(t, unit)
		for _, w := range warns {
			if strings.Contains(w.Msg, strings.SplitN(unit, "=", 2)[0]) {
				t.Errorf("%s: %s %s", unit, w.Level, w.Msg)
			}
		}
	}
}

// Every lifecycle directive reaches the registry, not the bespoke switch:
// WatchdogSec= and FileDescriptorStoreMax= had cases that only said "map
// it by hand".
func TestSystemdLifecycleRegistered(t *testing.T) {
	for _, k := range []string{"WatchdogSec", "FileDescriptorStoreMax", "KillMode", "SuccessExitStatus", "RemainAfterExit"} {
		if _, ok := sdServiceRules[k]; !ok {
			t.Errorf("%s has no rule", k)
		}
	}
	_, warns, _ := lcConvert(t, "FileDescriptorStoreMax=4")
	if lcWarned(warns, "NOTE", "review manually") {
		t.Error("FileDescriptorStoreMax= still reaches its bespoke case")
	}
}

// The tables are systemd's: spot-check them against the numbers in
// src/shared/exit-status.h and signal-util.c.
func TestSystemdLifecycleTables(t *testing.T) {
	for name, code := range map[string]int{"CHDIR": 200, "STDERR": 222, "PAM": 224, "RUNTIME_DIRECTORY": 233,
		"CHOWN": 235, "MEMORY_THP": 246, "EXCEPTION": 255, "CONFIG": 78, "NOTRUNNING": 7} {
		if lcExitStatusNames[name] != code {
			t.Errorf("%s = %d, want %d", name, lcExitStatusNames[name], code)
		}
	}
	// Every name the conv emits is one slinit's parser takes.
	for n := range lcSignalNames {
		if _, err := parseSignal("SIG" + n); err != nil {
			t.Errorf("slinit does not know SIG%s: %v", n, err)
		}
	}
	for in, want := range map[string]string{"RTMIN": "34", "RTMAX": "64", "SIGRTMIN+30": "64", "RTMAX-30": "34", "64": "64"} {
		if got, err := lcSignalFrom(in); err != nil || got != want {
			t.Errorf("%s → %q, %v; want %s", in, got, err, want)
		}
	}
	for _, in := range []string{"RTMIN+31", "RTMIN-1", "RTMAX+1", "65", "TERM ", "Term"} {
		if got, err := lcSignalFrom(in); err == nil {
			t.Errorf("%q → %q, want an error", in, got)
		}
	}
}

// The two exit status lists are kept apart: an empty assignment of one
// left the other's values out of normal-exit.
func TestSystemdExitListsOrder(t *testing.T) {
	sdOrders(t, "RestartPreventExitStatus=42\nRestartPreventExitStatus=", "SuccessExitStatus=143", func(t *testing.T, r sdOrderResult) {
		sdWant(t, r, "normal-exit", "143")
		sdWarn(t, r, "NOTE", "SuccessExitStatus=143")
	})
	sdOrders(t, "SuccessExitStatus=143\nSuccessExitStatus=", "RestartPreventExitStatus=42", func(t *testing.T, r sdOrderResult) {
		sdWant(t, r, "normal-exit", "42")
		if exHas(r.warns, "NOTE", "SuccessExitStatus=") || exHas(r.warns, "WARN", "SuccessExitStatus=") {
			t.Errorf("%s: a note for a reset SuccessExitStatus=: %v", r.how, r.warns)
		}
	})
	sdOrders(t, "SuccessExitStatus=143 SIGTERM", "RestartPreventExitStatus=143 7", func(t *testing.T, r sdOrderResult) {
		if got := exLines(r.out, "normal-exit"); len(got) != 1 || len(strings.Fields(got[0])) != 3 {
			t.Errorf("%s: normal-exit = %q, want 7, 143 and SIGTERM once each", r.how, got)
		}
	})
}

// SuccessExitStatus= beside an action or a restart that sees the exit
// as a failure is a WARN, wherever the other line is.
func TestSystemdSuccessExitStatusWarns(t *testing.T) {
	sdOrders(t, "SuccessExitStatus=143", "FailureAction=reboot", func(t *testing.T, r sdOrderResult) {
		sdWarn(t, r, "WARN", "SuccessExitStatus=143", "failure-action = reboot")
	})
	sdOrders(t, "SuccessExitStatus=143", "Restart=always", func(t *testing.T, r sdOrderResult) {
		sdWarn(t, r, "WARN", "SuccessExitStatus=143", "restart = yes")
	})
	sdOrders(t, "SuccessExitStatus=143", "Restart=on-failure", func(t *testing.T, r sdOrderResult) {
		sdWarn(t, r, "NOTE", "SuccessExitStatus=143")
		if exHas(r.warns, "WARN", "SuccessExitStatus=") {
			t.Errorf("%s: WARN for on-failure: %v", r.how, r.warns)
		}
	})
}

// RuntimeMaxSec= is ignored on a oneshot in systemd, wherever Type= is;
// with Restart= it is carried, as runtime-max-sec now fails the run.
func TestSystemdRuntimeMaxOrder(t *testing.T) {
	sdOrders(t, "RuntimeMaxSec=1d", "Type=oneshot", func(t *testing.T, r sdOrderResult) {
		sdWant(t, r, "runtime-max-sec")
		sdWarn(t, r, "NOTE", "RuntimeMaxSec=1d", "Type=oneshot")
	})
	sdOrders(t, "RuntimeMaxSec=1d", "Restart=always", func(t *testing.T, r sdOrderResult) {
		sdWant(t, r, "runtime-max-sec", "86400s")
	})
}

// BusName= gates readiness in slinit, so only Type=dbus carries it.
func TestSystemdBusNameOrder(t *testing.T) {
	sdOrders(t, "BusName=org.example.Ord", "Type=dbus", func(t *testing.T, r sdOrderResult) {
		sdWant(t, r, "bus-name", "org.example.Ord")
		if r.desc.BusName != "org.example.Ord" {
			t.Errorf("%s: BusName = %q", r.how, r.desc.BusName)
		}
	})
	sdOrders(t, "BusName=org.example.Ord", "Type=notify", func(t *testing.T, r sdOrderResult) {
		sdWant(t, r, "bus-name")
		sdWarn(t, r, "NOTE", "BusName=org.example.Ord", "not carried")
		if r.desc.BusName != "" {
			t.Errorf("%s: BusName = %q", r.how, r.desc.BusName)
		}
	})
}

// RestartForceExitStatus=0 is kept: slinit's IsForceRestartExit takes
// it. On a oneshot it restarts every successful run, which is a WARN.
func TestSystemdRestartForceZero(t *testing.T) {
	emitted, _, desc := lcConvert(t, "RestartForceExitStatus=0 3")
	if !slices.Contains(emitted, "restart-force-exit-status = 0 3") || !slices.Equal(desc.RestartForceExitCodes, []int{0, 3}) {
		t.Errorf("emitted %q, parsed %v", emitted, desc.RestartForceExitCodes)
	}
	sdOrders(t, "RestartForceExitStatus=0", "Type=oneshot", func(t *testing.T, r sdOrderResult) {
		sdWarn(t, r, "WARN", "RestartForceExitStatus=", "oneshot")
	})
}

// An empty signal assignment resets it; exit as an action gets a note.
func TestSystemdLifecycleNits(t *testing.T) {
	for _, k := range []string{"FinalKillSignal", "RestartKillSignal", "WatchdogSignal"} {
		emitted, warns, _ := lcConvert(t, k+"=SIGQUIT\n"+k+"=")
		for _, l := range emitted {
			if strings.Contains(l, "-signal = ") {
				t.Errorf("%s= did not reset: %q", k, l)
			}
		}
		if lcWarned(warns, "WARN", k) {
			t.Errorf("%s=: %v", k, warns)
		}
	}
	if _, warns, _ := lcConvert(t, "FailureAction=exit"); !lcWarned(warns, "NOTE", "powers off", "PID 1") {
		t.Errorf("FailureAction=exit: %v", warns)
	}
}

func TestSystemdSendSIGHUP(t *testing.T) {
	for val, want := range map[string]bool{"yes": true, "true": true, "no": false} {
		emitted, warns, desc := lcConvert(t, "SendSIGHUP="+val)
		if desc.SendSIGHUP != want {
			t.Errorf("SendSIGHUP=%s: send-sighup %v, want %v (emitted %q)", val, desc.SendSIGHUP, want, emitted)
		}
		for _, w := range warns {
			if strings.Contains(w.Msg, "SendSIGHUP") {
				t.Errorf("SendSIGHUP=%s warned: %v", val, w)
			}
		}
	}
}

func TestSystemdRestartSteps(t *testing.T) {
	for _, tc := range []struct {
		lines        string
		steps        int
		delay, cap   time.Duration
		warnContains string
	}{
		{lines: "RestartSec=1\nRestartSteps=5\nRestartMaxDelaySec=1min", steps: 5, delay: time.Second, cap: time.Minute},
		// In either order, in a drop-in or not: decided once the unit is read.
		{lines: "RestartMaxDelaySec=30s\nRestartSteps=3\nRestartSec=2", steps: 3, delay: 2 * time.Second, cap: 30 * time.Second},
		// No RestartSec=: systemd's 100ms.
		{lines: "RestartSteps=4\nRestartMaxDelaySec=10", steps: 4, delay: 100 * time.Millisecond, cap: 10 * time.Second},
		{lines: "RestartSteps=4", warnContains: "without RestartMaxDelaySec"},
		{lines: "RestartMaxDelaySec=10", warnContains: "without RestartSteps"},
		{lines: "RestartSteps=4\nRestartMaxDelaySec=infinity", warnContains: "without RestartMaxDelaySec"},
		// Below RestartSec=: RestartSec= drops to it and there is no curve.
		{lines: "RestartSec=20\nRestartSteps=4\nRestartMaxDelaySec=5", delay: 5 * time.Second, warnContains: "not above RestartSec"},
		{lines: "RestartSec=0\nRestartSteps=4\nRestartMaxDelaySec=5", warnContains: "RestartSec=0"},
		{lines: "RestartSteps=lots\nRestartMaxDelaySec=5", warnContains: "not 0 to 3000"},
	} {
		_, warns, desc := lcConvert(t, tc.lines)
		if desc.RestartSteps != tc.steps || (tc.delay != 0 && desc.RestartDelay != tc.delay) || (tc.cap != 0 && desc.RestartDelayCap != tc.cap) {
			t.Errorf("%q: steps %d delay %v cap %v, want %d %v %v", tc.lines, desc.RestartSteps, desc.RestartDelay, desc.RestartDelayCap, tc.steps, tc.delay, tc.cap)
		}
		if tc.warnContains != "" {
			found := false
			for _, w := range warns {
				found = found || strings.Contains(w.Msg, tc.warnContains)
			}
			if !found {
				t.Errorf("%q: no warning containing %q in %v", tc.lines, tc.warnContains, warns)
			}
		}
	}
}

func TestSystemdOneshotRestart(t *testing.T) {
	for _, order := range []string{"Type=oneshot\nRestart=on-failure", "Restart=on-failure\nType=oneshot"} {
		_, warns, desc := lcConvert(t, order)
		if desc.AutoRestart != service.RestartNever || !lcWarned(warns, "WARN", "not carried") {
			t.Errorf("%q: restart %v, warnings %v", order, desc.AutoRestart, warns)
		}
	}
	for _, v := range []string{"always", "on-success"} {
		p := writeUnit(t, t.TempDir(), "o.service", "[Service]\nType=oneshot\nExecStart=/bin/true\nRestart="+v+"\n")
		if _, _, err := ConvertSystemdUnit(p); err == nil || !strings.Contains(err.Error(), "not allowed for Type=oneshot") {
			t.Errorf("Restart=%s on a oneshot: err %v, want refused", v, err)
		}
	}
}

// Several ExecStartPre= lines run in turn from one pre-start-command, as
// systemd runs them: each must succeed unless "-", a failure stops the
// rest, and a lone $VAR that is empty is no argument.
func TestSystemdExecStartPreFolded(t *testing.T) {
	run := func(t *testing.T, lines string) (string, error, []SystemdWarning) {
		t.Helper()
		f := filepath.Join(t.TempDir(), "out")
		_, warns, desc := lcConvert(t, strings.ReplaceAll(lines, "OUT", f))
		if len(desc.PreStartCommand) == 0 {
			t.Fatalf("no pre-start-command: %v", warns)
		}
		err := exec.Command(desc.PreStartCommand[0], desc.PreStartCommand[1:]...).Run()
		b, _ := os.ReadFile(f)
		return string(b), err, warns
	}
	out, err, warns := run(t, "ExecStartPre=/bin/sh -c 'echo a >> OUT'\nExecStartPre=-/bin/false\nExecStartPre=/bin/sh -c 'echo b >> OUT'")
	if out != "a\nb\n" || err != nil || !lcWarned(warns, "NOTE", "3 ExecStartPre= lines run in turn") {
		t.Errorf("folded: out %q err %v warns %v", out, err, warns)
	}
	out, err, _ = run(t, "ExecStartPre=/bin/sh -c 'echo a >> OUT'\nExecStartPre=/bin/false\nExecStartPre=/bin/sh -c 'echo b >> OUT'")
	if out != "a\n" || err == nil {
		t.Errorf("a failing step: out %q err %v, want the rest skipped and a failure", out, err)
	}
	out, _, _ = run(t, "ExecStartPre=/bin/true\nExecStartPre=/bin/sh -c 'echo $$# > OUT' sh $SLINIT_TEST_UNSET_VAR")
	if strings.TrimSpace(out) != "0" {
		t.Errorf("an empty lone $VAR became %s argument(s), want 0", strings.TrimSpace(out))
	}
	_, warns, _ = lcConvert(t, "ExecStartPre=/bin/a\nExecStartPre=+/bin/b")
	if !lcWarned(warns, "WARN", "with and without") {
		t.Errorf("mixed +: %v", warns)
	}
}
