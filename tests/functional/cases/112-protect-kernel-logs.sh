#!/bin/sh
# Test: protect-kernel-logs blocks syslog(2) + hides /dev/kmsg.
#
# What the probe proves (it used to print OK on both dmesg branches, so
# it could not fail):
#   - busybox dmesg is a klogctl() = syslog(2) caller. Inside the guarded
#     service it must exit non-zero with EPERM ("not permitted"); in a
#     CONTROL service that is identical except for the directive, it
#     must succeed. The control is what makes the refusal mean
#     something: without it a dmesg that fails for any other reason
#     would read as protection.
#   - the runner binds /dev/null over /dev/kmsg in the service's private
#     mount namespace, so the service's mountinfo has a /dev/kmsg mount
#     point and PID 1's does not.

SVC="test-pkl"
CTL="test-pkl-control"

rm -f /run/pkl-svc-out /run/pkl-ctl-out

# The probe records dmesg's exit status and its stderr; stdout (the
# kernel ring itself) is thrown away. $$ keeps slinit's load-time
# expansion from eating the shell variables.
cat > "/etc/slinit.d/$SVC" <<'EOF'
type = process
protect-kernel-logs = yes
command = /bin/sh -c 'dmesg >/dev/null 2>/run/pkl-svc-err; echo rc=$$? > /run/pkl-svc-out; while :; do sleep 60; done'
restart = no
EOF

cat > "/etc/slinit.d/$CTL" <<'EOF'
type = process
command = /bin/sh -c 'dmesg >/dev/null 2>/run/pkl-ctl-err; echo rc=$$? > /run/pkl-ctl-out; while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
slinitctl --system start "$CTL" 2>/dev/null
wait_for_service "$SVC" STARTED 10
wait_for_service "$CTL" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"
assert_service_state "$CTL" "STARTED" "control service reached STARTED"

# STARTED is emitted right after fork for type=process without notify,
# which races with slinit-runner's seccomp Install() — /proc/PID/status
# can briefly show Seccomp=0 in the window between fork and the
# runner's install. Poll for up to 2s (same shape as 116-lock-personality).
_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ { print $2; exit }')
_seccomp=0
for _ in 1 2 3 4 5 6 7 8 9 10; do
    _seccomp=$(awk '/^Seccomp:/ { print $2 }' "/proc/$_pid/status" 2>/dev/null)
    [ "$_seccomp" = "2" ] && break
    sleep 0.2
done
assert_eq "$_seccomp" "2" "seccomp filter (mode 2) installed on child"

# Both probes run after STARTED; wait for their witnesses.
for _ in 1 2 3 4 5 6 7 8 9 10; do
    [ -s /run/pkl-svc-out ] && [ -s /run/pkl-ctl-out ] && break
    sleep 0.5
done

_ctl=$(cat /run/pkl-ctl-out 2>/dev/null)
_svc=$(cat /run/pkl-svc-out 2>/dev/null)
_svc_err=$(cat /run/pkl-svc-err 2>/dev/null)
echo "  diag: control '$_ctl' ($(cat /run/pkl-ctl-err 2>/dev/null)); guarded '$_svc' ($_svc_err)"

assert_eq "$_ctl" "rc=0" "control (no directive): dmesg reads the kernel log"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_svc" ] && [ "$_svc" != "rc=0" ]; then
    echo "OK: dmesg refused inside the guarded service ($_svc)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: dmesg inside the guarded service gave '$_svc', expected a non-zero rc"
fi
assert_contains "$_svc_err" "not permitted" "the refusal is EPERM from the syslog(2) filter"

# /dev/kmsg: bind-mounted (over by /dev/null) in the service's mount ns
# only. Field 5 of mountinfo is the mount point.
_svc_kmsg=$(awk '$5 == "/dev/kmsg"' "/proc/$_pid/mountinfo" 2>/dev/null | wc -l)
_host_kmsg=$(awk '$5 == "/dev/kmsg"' /proc/1/mountinfo 2>/dev/null | wc -l)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_svc_kmsg" -ge 1 ]; then
    echo "OK: /dev/kmsg is a mount point in the service's namespace"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no /dev/kmsg mount in the service's mountinfo"
fi
assert_eq "$_host_kmsg" "0" "/dev/kmsg bind did not leak to PID 1's namespace"

slinitctl --system stop "$CTL" >/dev/null 2>&1
slinitctl --system stop "$SVC" >/dev/null 2>&1

test_summary
