#!/bin/sh
# Test: command-argv0 = NAME overrides argv[0] presented to the child
# without changing the exec path. Analogue of runit's chpst -b. Proven
# via /proc/PID/cmdline (first NUL-terminated field is argv[0] verbatim)
# and /proc/PID/exe (still the real binary).
#
# Busybox limitation: on Alpine most /bin/* commands are busybox
# multi-call symlinks that DISPATCH on argv[0]. Overriding argv[0] to
# a name busybox doesn't recognise makes the process exit with
# "applet not found" — argv[0] passthrough is exercised but the target
# refuses to run so we can't inspect it. Skip when we detect busybox
# on /bin/sleep; the acceptance suite covers this on Void where sleep
# is real coreutils.
if readlink -f /bin/sleep 2>/dev/null | grep -q busybox; then
    echo "SKIP: /bin/sleep is busybox — argv[0] override causes 'applet not found'"
    test_summary
    return 0
fi

SVC="test-argv0"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
command = /bin/sleep 60
command-argv0 = my-fancy-argv0
restart = false
EOF
slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ {print $2; exit}')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_pid" ] || ! [ -d "/proc/$_pid" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: could not read PID from status (got '$_pid')"
    test_summary
    return 1
fi
echo "OK: child PID=$_pid"

_argv0=$(tr '\0' '\n' < "/proc/$_pid/cmdline" | head -1)
assert_eq "$_argv0" "my-fancy-argv0" "argv[0] as seen by kernel"

_exe=$(readlink "/proc/$_pid/exe" 2>/dev/null)
assert_contains "$_exe" "sleep" "exe path is still the real binary"

test_summary
