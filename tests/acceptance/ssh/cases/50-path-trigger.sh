#!/bin/sh
# 50-path-trigger — `start-on-path-exists = /trigger` arms a fanotify/
# inotify watcher in slinit (record.go:1227 SetStartOnPath); when the
# watched path appears, slinit auto-starts the service. The companion
# `manual = yes` keeps the service out of any default-start set so the
# trigger is the *only* way it enters STARTED.
#
# Probe: deploy a service whose path-trigger is not present at start
# time. Verify state == STOPPED. Create the path. Verify state
# transitions to STARTED within a few seconds.

SVC="acceptance-test-pathtrig"
TRIGGER="/run/acceptance-test-pathtrig.flag"
MARK="/run/acceptance-test-pathtrig.mark"

cleanup() {
    # Remove $TRIGGER BEFORE svc_remove: the pathRearmListener fires on
    # EventStopped, which re-arms the watcher; if the trigger file still
    # exists at that moment, the watcher re-fires synchronously and the
    # subsequent unload silently fails (service is started again).
    rm -f "$TRIGGER" "$MARK"
    sleep 1
    svc_remove "$SVC"
}
trap cleanup EXIT INT TERM

rm -f "$TRIGGER" "$MARK"

svc_deploy "$SVC" <<EOF
type = process
manual = yes
start-on-path-exists = $TRIGGER
command = /bin/sh -c 'touch $MARK; while :; do sleep 60; done'
restart = false
EOF

# Triggering load without starting: the service should be loaded into
# slinit's registry but stay STOPPED. `slinitctl status` resolves the
# description without forcing the desired state to start.
slinitctl --system status "$SVC" >/dev/null 2>&1
sleep 1

_st=$(svc_state "$SVC")
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_st" in
    STOPPED|"")
        echo "OK: $SVC parked at '$_st' before trigger appears"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: manual=yes service is '$_st' before trigger (expected STOPPED)"
        ;;
esac

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MARK" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: marker $MARK exists before trigger"
else
    echo "OK: no marker before trigger"
fi

# Fire the trigger. slinit should notice and start the service.
: > "$TRIGGER"

# Poll up to 8s for STARTED. fanotify/inotify wake-up is sub-second on
# Linux; 8s is generous for any boot-time scheduling jitter.
_e=0
while [ "$_e" -lt 8 ]; do
    if [ "$(svc_state "$SVC")" = "STARTED" ]; then break; fi
    sleep 1
    _e=$((_e + 1))
done
assert_service_state "$SVC" "STARTED" "$SVC STARTED after trigger touched"

# Confirm via marker that the *actual* command ran (not a phantom
# STARTED state).
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MARK" ]; then
    echo "OK: marker present — command ran after path trigger"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: marker missing despite STARTED state"
fi

test_summary
