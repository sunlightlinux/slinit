#!/bin/sh
# 233-restart-on-success — `restart = on-success` keeps a service going
# while it exits 0 and stops at the first failure.
#
# The inverse of on-failure, adapted from launchd's
# KeepAlive{SuccessfulExit: true}. The counter file is how repetition is
# observed without racing the state machine: each run appends a byte, so
# the file's size is the number of runs.

COUNTER=/run/ros-count
rm -f "$COUNTER"

cat > /etc/slinit.d/test-ros <<'EOF'
type = process
restart = on-success
restart-delay = 0.05
command = /bin/sh -c 'printf x >> /run/ros-count; exit 0'
EOF

slinitctl --system --no-wait start test-ros 2>/dev/null

# Wait for repetition rather than a state: a service that exits
# immediately is never observably STARTED for long.
_i=0
while [ "$_i" -lt 60 ]; do
    [ -s "$COUNTER" ] && [ "$(wc -c < "$COUNTER")" -ge 3 ] && break
    sleep 0.25
    _i=$((_i + 1))
done
_runs=$(wc -c < "$COUNTER" 2>/dev/null || echo 0)
echo "INFO: clean-exit runs observed: $_runs"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_runs" -ge 3 ]; then
    echo "OK: on-success restarted a cleanly exiting service ($_runs runs)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: only $_runs run(s) — on-success did not restart on exit 0"
fi

slinitctl --system --ignore-unstarted stop test-ros 2>/dev/null
slinitctl --system unload test-ros 2>/dev/null
rm -f /etc/slinit.d/test-ros

# The other half: a failure must end the cycle. This is what on-failure
# cannot express, so it is the assertion that matters.
rm -f "$COUNTER"
cat > /etc/slinit.d/test-ros-fail <<'EOF'
type = process
restart = on-success
restart-delay = 0.05
command = /bin/sh -c 'printf x >> /run/ros-count; exit 3'
EOF

slinitctl --system --no-wait start test-ros-fail 2>/dev/null
sleep 3
_fruns=$(wc -c < "$COUNTER" 2>/dev/null || echo 0)
echo "INFO: failing runs observed: $_fruns"
assert_eq "$_fruns" "1" "a failure ended the cycle after exactly one run"

slinitctl --system --ignore-unstarted stop test-ros-fail 2>/dev/null
slinitctl --system unload test-ros-fail 2>/dev/null
rm -f /etc/slinit.d/test-ros-fail "$COUNTER"

test_summary
