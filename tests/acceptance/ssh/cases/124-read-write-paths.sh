#!/bin/sh
# 124-read-write-paths — carves a writable exception out of what
# protect-system= would otherwise make read-only.

SVC="${ACCEPTANCE_NS_PREFIX}rwpath"
WORK="/tmp/acceptance-rwpath"

cleanup() {
    svc_remove "$SVC"
    rm -rf "$WORK"
}
trap cleanup EXIT INT TERM
cleanup
mkdir -p "$WORK"

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
protect-system = strict
read-write-paths = $WORK
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^  PID:/ { print $2 }')
sleep 0.3

# Grepping mountinfo: the entry for $WORK should NOT carry `ro`,
# meaning the read-write bind overrode the protect-system=strict
# blanket.
_TESTS_RUN=$((_TESTS_RUN + 1))
_entry=$(grep " $WORK " /proc/$_pid/mountinfo 2>/dev/null | head -1)
case "$_entry" in
    *"ro,"*|*"ro "*|*",ro,"*|*" ro"*|*",ro"*)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: $WORK still marked ro: $_entry"
        ;;
    "")
        # Mount may not appear as a separate entry when no override was
        # needed. Confirm the child can actually write.
        if nsenter -m -t "$_pid" sh -c "touch $WORK/probe" 2>/dev/null; then
            rm -f "$WORK/probe"
            echo "OK: $WORK writable inside namespace"
        else
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: could not write to $WORK inside namespace"
        fi
        ;;
    *)
        echo "OK: $WORK writable entry in namespace ($_entry)"
        ;;
esac

test_summary
