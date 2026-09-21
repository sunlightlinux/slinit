#!/bin/sh
# PID 1 must not die from a signal a local root can send it.
#
# Not from the nosystemd list — this came out of the question "in
# emergency mode, can a keypress kill PID 1 and panic the kernel?".
#
# The answer is no, by two separate mechanisms:
#
#  1. The keyboard cannot reach PID 1 at all. runRescueShell starts the
#     shell with Setsid+Setctty, so it owns /dev/console and its own
#     process group is the terminal's foreground group. Ctrl-C, Ctrl-\
#     and Ctrl-Z go there; PID 1 is in a different session entirely.
#
#  2. Signals sent explicitly ARE delivered — the kernel only drops the
#     ones PID 1 has no handler for, and the Go runtime installs a
#     handler for everything at startup, so that protection never
#     applies to us. pkg/eventloop/signals.go therefore claims each one
#     and gives it a deliberate meaning.
#
# This case covers the half of that set whose deliberate meaning is
# "keep running": SIGUSR1 reopens the control socket, SIGHUP is noted
# and ignored, SIGPIPE was never a shutdown trigger, and SIGKILL/SIGSTOP
# the kernel refuses outright for PID 1.
#
# The other half — TERM, INT, QUIT, USR2 — deliberately bring the system
# down (loop.go:280-345), so "did PID 1 survive" is the wrong question
# to ask of them and sending one here just reboots the VM mid-suite.
# They are covered where the distinction is actually observable:
#   - that they stay claimed at all: TestShutdownSignalSet in
#     pkg/eventloop, which is what catches a refactor dropping one
#     (an unclaimed SIGQUIT means Go's fatal default, and exit(2) as
#     PID 1 is a kernel panic);
#   - that they work from the keyboard, including at a recovery prompt:
#     tests/functional/cad-recovery-test.sh, driven from the host
#     because the guest cannot watch its own reboot.
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

# Claimed, and handled as a no-op or a side effect rather than a
# shutdown. SIGUSR1 reopens the control socket, which the probe's own
# `slinitctl list` then proves is still serving.
_survives USR1
_survives HUP

# Never a shutdown trigger. Go ignores a SIGPIPE that did not come from
# a write to fd 1 or 2, so an explicit kill -PIPE must be inert.
_survives PIPE

# SIGKILL and SIGSTOP cannot be sent to PID 1 at all — the kernel
# refuses them outright rather than merely ignoring them.
_survives KILL
_survives STOP

# Still healthy afterwards: not just alive, but supervising.
out=$(slinitctl list 2>&1)
assert_contains "$out" "boot" "service graph intact after the signal storm"

test_summary
