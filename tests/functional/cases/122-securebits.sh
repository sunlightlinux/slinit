#!/bin/sh
# Test: securebits = keep-caps,no-setuid-fixup,noroot reaches the
# child's kernel state.
#
# This used to check only that the child was alive, which it is with
# or without the directive. noroot gives an observable effect: with
# SECBIT_NOROOT set, a uid-0 process that execs a binary with no file
# capabilities gets no "root gets every capability" fixup, so the
# service's sh ends up with CapEff = 0. Without the bit (or if the
# runner never applies the bits) root's execve hands back the full set
# and CapEff is non-zero.

SVC="test-sbits"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
securebits = keep-caps,no-setuid-fixup,noroot
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ { print $2; exit }')

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid" ] && [ -d "/proc/$_pid" ]; then
    echo "OK: securebits config accepted; child pid=$_pid alive"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: child vanished (pid='$_pid')"
fi

# STARTED comes right after fork, and the runner applies the bits and
# then execs the command — poll until the pid is the command (CapEff of
# the runner itself is still the full set).
_capeff=""
for _ in $(seq 50); do   # up to 10 s: TCG can be slow to exec past the runner
    _capeff=$(awk '/^CapEff:/ { print $2 }' "/proc/$_pid/status" 2>/dev/null)
    [ "$_capeff" = "0000000000000000" ] && break
    sleep 0.2
done
echo "  diag: CapEff=$_capeff comm=$(cat "/proc/$_pid/comm" 2>/dev/null)"
assert_eq "$_capeff" "0000000000000000" "noroot: uid-0 child has no effective capabilities"

slinitctl --system stop "$SVC" >/dev/null 2>&1

test_summary
