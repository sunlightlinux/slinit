#!/bin/sh
# Test: normal-exit = 42 makes an exit with code 42 count as success, so
# restart = yes does not respawn it.
#
# Two process services each append a line to a counter and exit: one
# with 42 (declared normal), a control with 41. The control must be
# restarted; the normal-exit one must run exactly once. (This used to be
# a scripted service with restart = no, which ends STOPPED with or
# without the directive.)

for s in test-nex test-nex-ctl; do
    rm -f "/run/$s.count"
done

cat > /etc/slinit.d/test-nex <<'EOF2'
type = process
command = /bin/sh -c "echo run >> /run/test-nex.count; exit 42"
normal-exit = 42
restart = yes
restart-delay = 0.2
EOF2
cat > /etc/slinit.d/test-nex-ctl <<'EOF2'
type = process
command = /bin/sh -c "echo run >> /run/test-nex-ctl.count; exit 41"
normal-exit = 42
restart = yes
restart-delay = 0.2
EOF2

slinitctl --system --no-wait start test-nex >/dev/null 2>&1
slinitctl --system --no-wait start test-nex-ctl >/dev/null 2>&1
sleep 3

runs=$(wc -l < /run/test-nex.count 2>/dev/null)
ctl=$(wc -l < /run/test-nex-ctl.count 2>/dev/null)
echo "INFO: exit 42 ran ${runs:-0} time(s), exit 41 ran ${ctl:-0} time(s)"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "${ctl:-0}" -ge 2 ]; then
    echo "OK: control (exit 41) was restarted"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: control (exit 41) ran ${ctl:-0} time(s); restart = yes did not respawn it"
fi
assert_eq "${runs:-0}" "1" "exit 42 is normal: not restarted"

slinitctl --system --ignore-unstarted stop test-nex-ctl >/dev/null 2>&1

test_summary
