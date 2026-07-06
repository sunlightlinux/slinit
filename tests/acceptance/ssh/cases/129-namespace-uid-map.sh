#!/bin/sh
# 129-namespace-uid-map + namespace-gid-map — verifies the child's
# uid/gid_map has our declared mapping.

SVC="${ACCEPTANCE_NS_PREFIX}uidmap"

cleanup() {
    svc_remove "$SVC"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
namespace-user = yes
namespace-uid-map = 0:100000:1000
namespace-gid-map = 0:100000:1000
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^  PID:/ { print $2 }')
sleep 0.3

_uid_map=$(cat "/proc/$_pid/uid_map" 2>/dev/null | tr -s ' ' | sed 's/^ //')
_gid_map=$(cat "/proc/$_pid/gid_map" 2>/dev/null | tr -s ' ' | sed 's/^ //')

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_uid_map" in
    "0 100000 1000"*)
        echo "OK: uid_map = '$_uid_map'"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: uid_map = '$_uid_map'"
        ;;
esac

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_gid_map" in
    "0 100000 1000"*)
        echo "OK: gid_map = '$_gid_map'"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: gid_map = '$_gid_map'"
        ;;
esac

test_summary
