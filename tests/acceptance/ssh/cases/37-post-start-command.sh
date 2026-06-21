#!/bin/sh
# 37-post-start-command — `post-start-command` runs asynchronously after
# the service reaches STARTED. A non-zero exit is logged but does NOT
# fail the service (systemd's ExecStartPost= semantics —
# pkg/service/process.go:1003-1010).

SVC="acceptance-test-poststart"
MARK_POST="/run/acceptance-test-poststart.post"

cleanup() {
    svc_remove "$SVC"
    rm -f "$MARK_POST"
}
trap cleanup EXIT INT TERM

rm -f "$MARK_POST"

# --- Sub-case A: post-start runs after main → marker appears ----------
svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
post-start-command = /bin/sh -c 'sleep 1; touch $MARK_POST'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED"

# Post-start is async — should appear within ~2s of STARTED.
_TESTS_RUN=$((_TESTS_RUN + 1))
_t=0
while [ "$_t" -lt 5 ]; do
    if [ -e "$MARK_POST" ]; then
        echo "OK: post-start marker appeared after ${_t}s"
        break
    fi
    sleep 1
    _t=$((_t + 1))
done
if [ ! -e "$MARK_POST" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: post-start marker never appeared"
fi

svc_remove "$SVC"
rm -f "$MARK_POST"

# --- Sub-case B: post-start failure does NOT fail the service ---------
svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
post-start-command = /bin/sh -c 'exit 9'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
# A reasonable amount of time for post-start to run and log.
sleep 2

_TESTS_RUN=$((_TESTS_RUN + 1))
_st=$(svc_state "$SVC")
case "$_st" in
    STARTED)
        echo "OK: post-start failure didn't take the service down (still STARTED)"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: post-start failed and service is now '$_st' (expected STARTED)"
        ;;
esac

test_summary
