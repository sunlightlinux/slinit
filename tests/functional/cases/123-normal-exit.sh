#!/bin/sh
# Test: normal-exit = 42 makes a scripted svc that exits 42 land in
# STOPPED (not FAILED).

SVC="test-nex"

cat > "/etc/slinit.d/$SVC" <<EOF
type = scripted
command = /bin/sh -c 'exit 42'
normal-exit = 42
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
sleep 2
_state=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/State:/ {print $2; exit}')

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_state" in
    STOPPED) echo "OK: exit 42 treated as normal → STOPPED" ;;
    *) _TESTS_FAILED=$((_TESTS_FAILED + 1)); echo "FAIL: expected STOPPED, got '$_state'" ;;
esac

test_summary
