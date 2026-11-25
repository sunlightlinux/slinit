#!/bin/sh
# Test: command-argv0 = NAME overrides argv[0] presented to the child
# without changing the exec path. Analogue of runit's chpst -b. Proven
# via /proc/PID/cmdline (first NUL-terminated field is argv[0] verbatim)
# and /proc/PID/exe (still the real binary).
#
# On Alpine /bin/sleep is a busybox multi-call link that DISPATCHES on
# argv[0]: overridden to a name it doesn't know, it exits with "applet not
# found" before anything can be inspected. This case used to skip there,
# which on the VM meant always. slinit-monitor, a Go binary that ignores
# argv[0] and stays up while it watches a service, stands in for it.
if readlink -f /bin/sleep 2>/dev/null | grep -q busybox; then
    _cmd="/usr/bin/slinit-monitor --system -c true boot"
    _exe_want="slinit-monitor"
else
    _cmd="/bin/sleep 60"
    _exe_want="sleep"
fi

SVC="test-argv0"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
command = $_cmd
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
assert_contains "$_exe" "$_exe_want" "exe path is still the real binary"

test_summary
