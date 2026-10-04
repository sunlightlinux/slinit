#!/bin/sh
# nosystemd checklist — systemd#6620.
#
# "services writing to stdout become silent after journal restart":
# systemd-journald is a separate process holding the read end of every
# service's stdout pipe. Restart it and already-running services write
# into a closed pipe — their output vanishes until they are themselves
# restarted. Silent log loss, no warning.
# https://github.com/systemd/systemd/issues/6620
#
# slinit has no such process. The log consumer (LogRotator) lives inside
# PID 1, so there is no separate daemon whose restart can orphan a
# writer. This test demonstrates the property that follows: the sink can
# turn over completely underneath a running producer — which is what a
# consumer restart amounts to — and the producer keeps being captured,
# without being restarted itself.

wait_for_service "chatty" "STARTED" 10

# Wait for the first output rather than sampling once after a fixed
# delay. This is a precondition — "is anything being captured at all" —
# not a measurement of the property under test, so it must not carry a
# timing assumption of its own.
#
# It did. `sleep 3` then one sample read zero on a CI runner while
# passing locally; the same run's other four assertions all passed, so
# output was flowing, the sink did turn over and the producer was never
# restarted. Only the warm-up sample was early. Measured under software
# emulation here: three seconds buys about 40 lines, against a counter
# that reaches five figures by the end of the case — a margin of 40 lines
# on a shared runner with eight shards in flight is no margin.
#
# Counted across the live file AND its rotations, for the same reason the
# counter check below is: at this rate the live file turns over several
# times a second, so a lone sample of it can legitimately catch a file
# with no newline-terminated line in it yet.
captured_lines() {
    cat /tmp/chatty.log /tmp/chatty.log.* 2>/dev/null | wc -l
}
lines_before=0
i=0
while [ "$i" -lt 30 ]; do
    lines_before=$(captured_lines)
    [ "$lines_before" -gt 0 ] && break
    i=$((i + 1))
    sleep 1
done

# Taken after the wait: this is the pid that must survive the turnover,
# and a warm-up that outlasted a restart would otherwise record one that
# was already gone.
pid_before=$(slinitctl status chatty 2>/dev/null | awk '/PID:/{print $2}')

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$lines_before" -gt 0 ]; then
    echo "OK: producer output is being captured ($lines_before lines after ${i}s)"
else
    echo "FAIL: no output captured in 30s — nothing reached the sink at all"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# Force the sink to turn over beneath the running writer. The producer
# writes in a tight loop with no sleep and logfile-max-size is 4096, so
# this rotates many times a second on its own; wait long enough to be
# sure at least one rotation happened.
rotated_seen=0
i=0
while [ "$i" -lt 15 ]; do
    if ls /tmp/chatty.log.* >/dev/null 2>&1; then
        rotated_seen=1
        break
    fi
    i=$((i + 1))
    sleep 1
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$rotated_seen" = "1" ]; then
    echo "OK: the log sink turned over under the running producer"
else
    echo "FAIL: sink never rotated — test did not exercise the property"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# The point of the test: after the sink turned over, output must still
# be arriving. Measured by the producer's own monotonic counter rather
# than by file size, because at this rate the live file rotates several
# times a second and its size can legitimately shrink between samples.
#
# Taken as the highest counter across the live file *and* its rotations,
# not from the live file alone. Reading only the live one samples an
# empty file whenever a rotation has just happened, and an empty sample
# reads as "the producer went silent" when it did not — which is how this
# case failed on a CI runner while passing locally. The rotations are
# plain text (no compression directive on the service), so tail works on
# all of them.
counter_of() {
    for f in /tmp/chatty.log /tmp/chatty.log.*; do
        [ -f "$f" ] || continue
        tail -n 1 "$f" 2>/dev/null | sed -n 's/^chatty-line-\([0-9]*\).*/\1/p'
    done | sort -n | tail -1
}
n_before=$(counter_of)
sleep 3
n_after=$(counter_of)

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$n_before" ] && [ -n "$n_after" ] && [ "$n_after" -gt "$n_before" ]; then
    echo "OK: output still flowing after the sink turned over ($n_before -> $n_after)"
else
    echo "FAIL: producer went silent after the sink turned over (systemd#6620)"
    echo "  counter before='$n_before' after='$n_after'"
    echo "  --- live file tail:"; tail -3 /tmp/chatty.log 2>&1 | sed 's/^/    /'
    echo "  --- sizes:"; ls -l /tmp/chatty.log* 2>&1 | sed 's/^/    /' | head -5
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# And it achieved that without restarting the producer — systemd's
# workaround for #6620 was "restart your services too".
pid_after=$(slinitctl status chatty 2>/dev/null | awk '/PID:/{print $2}')
assert_eq "$pid_after" "$pid_before" "producer was never restarted"
assert_service_state "chatty" "STARTED" "producer still running"

test_summary
