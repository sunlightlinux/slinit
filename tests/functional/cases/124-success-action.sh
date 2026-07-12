#!/bin/sh
# Test: success-action = none parses cleanly and a scripted svc that
# exits 0 lands in STARTED or STOPPED. System-wide reboot/poweroff
# actions are out of scope for the functional harness.

SVC="test-sxa"

cat > "/etc/slinit.d/$SVC" <<EOF
type = scripted
command = /bin/sh -c 'exit 0'
success-action = none
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
sleep 2
_state=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/State:/ {print $2; exit}')

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_state" in
    STARTED|STOPPED) echo "OK: success-action=none parsed; state='$_state'" ;;
    *) _TESTS_FAILED=$((_TESTS_FAILED + 1)); echo "FAIL: state='$_state'" ;;
esac

test_summary
