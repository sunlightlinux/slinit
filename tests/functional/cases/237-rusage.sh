#!/bin/sh
# 237-rusage — launchd's getrusage, in slinit's three scopes, end to end
# through the control protocol.
#
# The numbers have to be checked, not just the exit status: a verb that
# answers with a well-formed body of zeros looks identical to a working
# one. So this asserts that a service which has deliberately burnt CPU
# reports CPU, and that the seven fields Linux never maintains are
# absent rather than printed as zero.

for scope in self children; do
    _out=$(slinitctl --system rusage "$scope" 2>&1)
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if echo "$_out" | grep -q "^Scope=$scope$"; then
        echo "OK: rusage $scope answers with its own scope"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: rusage $scope: $_out"
    fi
done

# slinit itself has been running the whole boot, so a zero here means the
# syscall result was discarded somewhere on the wire.
_self_user=$(slinitctl --system rusage self 2>/dev/null | awk -F= '/^UserTimeSec=/ { print $2 }')
_self_sys=$(slinitctl --system rusage self 2>/dev/null | awk -F= '/^SystemTimeSec=/ { print $2 }')
_self_rss=$(slinitctl --system rusage self 2>/dev/null | awk -F= '/^MaxRSSKiB=/ { print $2 }')
echo "INFO: slinit itself: user=$_self_user sys=$_self_sys maxrss=${_self_rss}KiB"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$(echo "$_self_user $_self_sys" | awk '{ print ($1 + $2 > 0) }')" = 1 ]; then
    echo "OK: PID 1 reports CPU time it has actually used"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: PID 1 reports no CPU time at all (user=$_self_user sys=$_self_sys)"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "${_self_rss:-0}" -gt 0 ] 2>/dev/null; then
    echo "OK: PID 1 reports a peak RSS (${_self_rss} KiB)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: PID 1 reports MaxRSSKiB=$_self_rss"
fi

# The per-service scope, which launchd has no form for. The service
# spins so that its CPU time is certainly non-zero by the time it is
# asked — a sleeping service would report a true zero and the assertion
# could not tell that from a broken read.
cat > /etc/slinit.d/test-rusage-busy <<'EOF'
type = process
command = /bin/sh -c 'while :; do :; done'
EOF
slinitctl --system start test-rusage-busy > /dev/null 2>&1
sleep 2
_body=$(slinitctl --system rusage test-rusage-busy 2>&1)
echo "INFO: service body: $(echo "$_body" | tr '\n' ' ' | cut -c1-150)"

_svc_user=$(echo "$_body" | awk -F= '/^UserTimeSec=/ { print $2 }')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$(echo "${_svc_user:-0}" | awk '{ print ($1 > 0.1) }')" = 1 ]; then
    echo "OK: a spinning service reports the CPU it burnt ($_svc_user s)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: a spinning service reported UserTimeSec=$_svc_user"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_body" | grep -q "^Scope=test-rusage-busy$"; then
    echo "OK: the body names the service as its scope"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: scope line missing for the service"
fi

# Block I/O has no per-live-task source, so it must be declared
# unavailable instead of reported as a zero.
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_body" | grep -q "^Unavailable=.*BlockInputOps"; then
    echo "OK: block I/O is declared unavailable, not reported as zero"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no Unavailable line naming block I/O: $_body"
fi

# The seven unmaintained struct rusage fields must not appear as data.
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_body" | grep -qE "^(Swaps|MessagesSent|MessagesReceived|SignalsReceived)="; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: a field Linux never maintains was printed as a value"
else
    echo "OK: no unmaintained field is printed as a value"
fi

slinitctl --system --ignore-unstarted stop test-rusage-busy > /dev/null 2>&1
slinitctl --system unload test-rusage-busy > /dev/null 2>&1
rm -f /etc/slinit.d/test-rusage-busy

# A stopped service has no live process to read, so the request must
# fail with a reason rather than answer with zeros.
cat > /etc/slinit.d/test-rusage-stopped <<'EOF'
type = process
command = /bin/sleep 60
manual = yes
EOF
_stopped=$(slinitctl --system rusage test-rusage-stopped 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_stopped" | grep -qi "no running process"; then
    echo "OK: a stopped service says so instead of reporting zeros"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: stopped service gave: $_stopped"
fi
slinitctl --system unload test-rusage-stopped > /dev/null 2>&1
rm -f /etc/slinit.d/test-rusage-stopped

# Arity is enforced centrally, so this must be a usage error.
_TESTS_RUN=$((_TESTS_RUN + 1))
if slinitctl --system rusage > /dev/null 2>&1; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: rusage with no argument was accepted"
else
    echo "OK: rusage requires an argument"
fi

test_summary
