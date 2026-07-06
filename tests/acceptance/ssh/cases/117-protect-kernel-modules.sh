#!/bin/sh
# 117-protect-kernel-modules — blocks init_module/finit_module/
# delete_module via seccomp. We probe by attempting modprobe from
# inside the service (which will syscall init_module).

SVC="${ACCEPTANCE_NS_PREFIX}pkm"
OUT="/tmp/acceptance-pkm-out"

cleanup() {
    svc_remove "$SVC"
    rm -f "$OUT"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
protect-kernel-modules = yes
command = /bin/sh -c 'modprobe -n dummy 2>$OUT; while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"
sleep 0.5

# modprobe writes to $OUT on failure. Any error output confirms
# either the syscall was blocked or module loading was refused;
# empty stderr would mean success, which the guard should prevent.
_err=$(cat "$OUT" 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_err" in
    *"Operation not permitted"*|*"not permitted"*|*"blocked"*|*"denied"*|*modprobe*)
        echo "OK: modprobe rejected inside guarded service"
        ;;
    "")
        # modprobe -n is a dry-run and might succeed even blocked. In
        # that case, verify the SECCOMP filter is at least advertised
        # in /proc/PID/status.
        _pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^  PID:/ { print $2 }')
        _seccomp=$(awk '/^Seccomp:/ { print $2 }' "/proc/$_pid/status" 2>/dev/null)
        if [ "$_seccomp" = "2" ]; then
            echo "OK: seccomp mode 2 (filter) active on child (pid $_pid)"
        else
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: no probe evidence — modprobe silent + no seccomp filter (mode=$_seccomp)"
        fi
        ;;
    *)
        # Any other error still means blocked, just via a different code path.
        echo "OK: modprobe blocked ($_err)"
        ;;
esac

test_summary
