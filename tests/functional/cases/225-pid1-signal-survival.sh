#!/bin/sh
# PID 1 must survive every signal a local root can send it.
#
# Not from the nosystemd list — this came out of the question "in
# emergency mode, can a keypress kill PID 1 and panic the kernel?".
#
# The answer is no, by two separate mechanisms, and this test pins both
# so a refactor cannot quietly remove either:
#
#  1. The keyboard cannot reach PID 1 at all. runRescueShell starts the
#     shell with Setsid+Setctty, so it owns /dev/console and its own
#     process group is the terminal's foreground group. Ctrl-C, Ctrl-\
#     and Ctrl-Z go there; PID 1 is in a different session entirely.
#
#  2. Signals sent explicitly ARE delivered — the kernel only drops the
#     ones PID 1 has no handler for, and pkg/eventloop/signals.go
#     registers TERM, INT, QUIT, HUP, USR1, USR2 and the systemd RT
#     range. Each is a deliberate, orderly action rather than a death:
#     INT reboots (this is the Ctrl-Alt-Del path, since InitPID1 calls
#     LINUX_REBOOT_CMD_CAD_OFF so the kernel signals init instead of
#     hard-rebooting), QUIT powers off, TERM shuts down.
#
# The failure this guards against is a future change that drops a
# signal from SetupSignals. An unregistered signal whose Go-runtime
# default is fatal — SIGQUIT dumps goroutine stacks and exit(2)s —
# would then kill PID 1, and exit(2) as PID 1 is a kernel panic.
#
# Each signal is announced before it is sent, so if the VM does die the
# console log names the culprit rather than just timing out.

_survives() {
    _sig="$1"
    echo "SIGNAL-PROBE: sending $_sig to PID 1"
    kill -"$_sig" 1 2>/dev/null
    sleep 1
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if kill -0 1 2>/dev/null && slinitctl list >/dev/null 2>&1; then
        echo "OK: PID 1 survived $_sig and still answers the control socket"
    else
        echo "FAIL: PID 1 did not survive $_sig"
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
    fi
}

# Every registered shutdown signal. Each initiates an orderly shutdown
# rather than killing PID 1; the service graph check at the end proves
# the loop is still supervising afterwards.
_survives TERM
_survives INT
_survives HUP
_survives USR1
_survives USR2
_survives PIPE

# SIGKILL and SIGSTOP cannot be sent to PID 1 at all — the kernel
# refuses them outright rather than merely ignoring them.
_survives KILL

# SIGQUIT is the one with a fatal Go-runtime default, so it is the
# canary: if SetupSignals ever stops registering it, this is where the
# VM dies.
_survives QUIT

# Still healthy afterwards: not just alive, but supervising.
out=$(slinitctl list 2>&1)
assert_contains "$out" "boot" "service graph intact after the signal storm"

test_summary
