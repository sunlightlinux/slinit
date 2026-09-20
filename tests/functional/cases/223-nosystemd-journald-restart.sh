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
sleep 3

pid_before=$(slinitctl status chatty 2>/dev/null | awk '/PID:/{print $2}')

lines_before=$(wc -l < /tmp/chatty.log 2>/dev/null || echo 0)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$lines_before" -gt 0 ]; then
    echo "OK: producer output is being captured ($lines_before lines)"
else
    echo "FAIL: no output captured before the sink turned over"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# Force the sink to turn over beneath the running writer. At
# logfile-max-size=2048 with a line per second this rotates on its own;
# wait long enough to be sure at least one rotation happened.
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
# be arriving. Measure by the producer's own monotonic counter rather
# than by file size — at this rate the live file rotates several times a
# second, so its size can legitimately shrink between two samples.
counter_of() {
    tail -n 1 /tmp/chatty.log 2>/dev/null | sed -n 's/^chatty-line-\([0-9]*\).*/\1/p'
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
