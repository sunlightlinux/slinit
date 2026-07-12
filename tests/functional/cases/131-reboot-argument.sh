#!/bin/sh
# Test: parse-only smoke for reboot-argument. Actual reboot is out of
# scope; verifying the config loads and the service reaches a
# terminal state is enough.

SVC="test-rebarg"

cat > "/etc/slinit.d/$SVC" <<EOF
type = scripted
command = /bin/sh -c 'exit 0'
success-action = none
reboot-argument = recovery
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
sleep 2
_state=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/State:/ {print $2; exit}')

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_state" in
    STARTED|STOPPED) echo "OK: reboot-argument parsed cleanly; state='$_state'" ;;
    *) _TESTS_FAILED=$((_TESTS_FAILED + 1)); echo "FAIL: state='$_state'" ;;
esac

test_summary
