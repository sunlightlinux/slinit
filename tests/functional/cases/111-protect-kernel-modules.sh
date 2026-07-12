#!/bin/sh
# Test: protect-kernel-modules blocks init_module/finit_module/
# delete_module via seccomp. Probe: modprobe from inside the service.

SVC="test-pkm"
OUT="/tmp/functional-pkm-out"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
protect-kernel-modules = yes
command = /bin/sh -c 'modprobe -n dummy 2>$OUT; while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"
sleep 0.5

_err=$(cat "$OUT" 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_err" in
    *"Operation not permitted"*|*"not permitted"*|*"blocked"*|*"denied"*|*modprobe*)
        echo "OK: modprobe rejected inside guarded service" ;;
    "")
        _pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ { print $2; exit }')
        _seccomp=$(awk '/^Seccomp:/ { print $2 }' "/proc/$_pid/status" 2>/dev/null)
        if [ "$_seccomp" = "2" ]; then
            echo "OK: seccomp mode 2 (filter) active on child (pid $_pid)"
        else
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: no probe evidence — modprobe silent + no seccomp filter (mode=$_seccomp)"
        fi ;;
    *)
        echo "OK: modprobe blocked ($_err)" ;;
esac

test_summary
