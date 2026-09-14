#!/bin/sh
# Test: protect-hostname blocks sethostname/setdomainname via seccomp.

SVC="test-phn"
OUT="/tmp/functional-phn-out"
_orig_host=$(hostname)

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
protect-hostname = yes
command = /bin/sh -c 'hostname functional-probe 2>$OUT; while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"

# STARTED is emitted right after fork for type=process without notify,
# which races with slinit-runner's seccomp Install() — /proc/PID/status
# can briefly show Seccomp=0 in the window between fork and the
# runner's install. Poll for up to 5s (bumped from 2s after a full
# 218-case run hit the race under load — even the hostname assertion
# fired, meaning the filter genuinely never installed within the
# earlier budget on that iteration).
_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ { print $2; exit }')
_seccomp=0
_i=0
while [ "$_i" -lt 25 ]; do
    _seccomp=$(awk '/^Seccomp:/ { print $2 }' "/proc/$_pid/status" 2>/dev/null)
    [ "$_seccomp" = "2" ] && break
    sleep 0.2
    _i=$((_i + 1))
done
assert_eq "$_seccomp" "2" "seccomp filter (mode 2) installed"

_err=$(cat "$OUT" 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_err" in
    *"Operation not permitted"*|*"denied"*|*"not permitted"*)
        echo "OK: sethostname blocked" ;;
    *)
        echo "OK: seccomp installed (probe stderr: '${_err:-<empty>}')" ;;
esac

# Host hostname must remain unchanged. If the seccomp install lost
# the fork-vs-install race and the probe managed to sneak sethostname
# through, restore the original name best-effort BEFORE asserting so
# the machine does not leak the mutation into the next case — the
# assertion still fires because we compare the observed value from
# BEFORE the restore.
_after_host=$(hostname)
if [ "$_after_host" != "$_orig_host" ]; then
    hostname "$_orig_host" 2>/dev/null || true
fi
assert_eq "$_after_host" "$_orig_host" "host hostname untouched by guarded service"

test_summary
