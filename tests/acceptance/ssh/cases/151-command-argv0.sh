#!/bin/sh
# 151-command-argv0 — command-argv0 = NAME overrides the argv[0]
# presented to the child, without changing the executable path.
# Analogue of runit's chpst -b. Proven via /proc/PID/cmdline whose
# first NUL-terminated field is exactly argv[0] as passed by the
# parent — the kernel does not synthesize it from the binary path.

SVC="acceptance-test-argv0"
MARK=/tmp/acceptance-argv0.$$.pid

cleanup() {
    svc_remove "$SVC"
    rm -f "$MARK"
}
trap cleanup EXIT INT TERM

# Sleep is a stable process to inspect: unbuffered, no forks, argv[0]
# is passed straight through. We drop its PID into a marker so the
# assertion can read /proc/<PID>/cmdline without racing the state file.
svc_deploy "$SVC" <<EOF
type = process
command = /bin/sleep 60
command-argv0 = my-fancy-argv0
restart = false
EOF
slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED"

# Pull the PID from status.
_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ {print $2; exit}')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_pid" ] || ! [ -d "/proc/$_pid" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: could not read PID from status (got '$_pid')"
    test_summary
    return 1 2>/dev/null || exit 1
fi
echo "OK: child PID=$_pid"

# /proc/<PID>/cmdline is NUL-separated; argv[0] is the first field.
_argv0=$(tr '\0' '\n' < "/proc/$_pid/cmdline" | head -1)
assert_eq "$_argv0" "my-fancy-argv0" \
    "argv[0] as seen by kernel"

# Sanity: /proc/<PID>/exe still points at the real /bin/sleep — the
# override changes argv[0] only, not the exec target.
_exe=$(readlink "/proc/$_pid/exe" 2>/dev/null)
assert_contains "$_exe" "sleep" \
    "exe path is still the real binary (contains 'sleep')"

test_summary
