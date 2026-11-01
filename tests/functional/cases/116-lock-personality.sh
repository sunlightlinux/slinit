#!/bin/sh
# Test: lock-personality refuses changing the personality via seccomp.
#
# Checking only that a filter is installed (seccomp mode 2) is what let
# this setting refuse nothing for months: the filter was there, and
# empty, because "personality" was missing from the syscall table. So
# the service also runs busybox linux64 (personality(PER_LINUX), the
# current one: allowed) and linux32 (PER_LINUX32, a change: killed by
# SIGSYS, exit 159), and a control service without the lock must run
# linux32 fine, so the check can tell the two apart.

SVC="test-lockp"
CTL="test-lockp-off"

# A script, not the command line: `command =` expands $VAR itself.
cat > /tmp/lockp.sh <<'EOF'
#!/bin/sh
linux64 true; echo $? > "/run/$1.64"
linux32 true; echo $? > "/run/$1.32"
while :; do sleep 60; done
EOF
rm -f /run/$SVC.* /run/$CTL.*

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
lock-personality = yes
command = /bin/sh /tmp/lockp.sh $SVC
restart = no
EOF
cat > "/etc/slinit.d/$CTL" <<EOF
type = process
command = /bin/sh /tmp/lockp.sh $CTL
restart = no
EOF
slinitctl --system start "$CTL" 2>/dev/null

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"

# STARTED is emitted right after fork for type=process without notify
# (pkg/service/process.go: "No readiness protocol - mark as started
# immediately"), which races with slinit-runner's hardening setup —
# seccomp filter is installed just before the runner exec's the real
# command, so /proc/PID/status can briefly show Seccomp=0 in the
# window between fork and runner's Install() call. Poll the field
# for up to 5s to let the runner catch up on slow VMs — 2s passed in
# isolation but missed once mid-suite (2026-09-21), under full load.
_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ { print $2; exit }')
_seccomp=0
_tries=0
for _ in $(seq 1 25); do
    _tries=$((_tries + 1))
    _seccomp=$(awk '/^Seccomp:/ { print $2 }' "/proc/$_pid/status" 2>/dev/null)
    [ "$_seccomp" = "2" ] && break
    sleep 0.2
done
echo "MEASURE: seccomp became $_seccomp after $_tries poll(s) of 0.2s"
assert_eq "$_seccomp" "2" "seccomp filter (mode 2) installed"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -d "/proc/$_pid" ]; then
    echo "OK: service pid $_pid still running under lock-personality"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: pid $_pid vanished"
fi

# The script writes its markers right after the runner's exec.
for _ in $(seq 1 25); do
    [ -s "/run/$SVC.32" ] && [ -s "/run/$CTL.32" ] && break
    sleep 0.2
done
assert_eq "$(cat /run/$SVC.64 2>/dev/null)" "0" "locked: setting the current personality is allowed"
assert_eq "$(cat /run/$SVC.32 2>/dev/null)" "159" "locked: changing the personality is killed by SIGSYS"
assert_eq "$(cat /run/$CTL.32 2>/dev/null)" "0" "unlocked control: changing the personality works"

slinitctl --system stop "$SVC" >/dev/null 2>&1
slinitctl --system stop "$CTL" >/dev/null 2>&1
test_summary
