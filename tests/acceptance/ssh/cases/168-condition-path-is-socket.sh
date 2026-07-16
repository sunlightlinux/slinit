#!/bin/sh
# 168-condition-path-is-socket — S_ISSOCK on stat(). Positive path uses
# slinit's own control socket at /run/slinit.socket (always present on
# a running target); negative uses /etc/passwd (regular file); missing
# path is exercised implicitly by a bogus location.

SVC_SOCK="acceptance-test-cond-sock-sock"
SVC_REG="acceptance-test-cond-sock-reg"
SVC_MISS="acceptance-test-cond-sock-miss"
MARK_SOCK="/run/acceptance-cond-sock.mark"
MARK_REG="/run/acceptance-cond-sock-reg.mark"
MARK_MISS="/run/acceptance-cond-sock-miss.mark"
SLINIT_SOCK="/run/slinit.socket"
NOT_A_SOCK="/etc/passwd"
MISSING="/nonexistent/acceptance-168.sock"

cleanup() {
    svc_remove "$SVC_SOCK" "$SVC_REG" "$SVC_MISS"
    rm -f "$MARK_SOCK" "$MARK_REG" "$MARK_MISS"
}
trap cleanup EXIT INT TERM
rm -f "$MARK_SOCK" "$MARK_REG" "$MARK_MISS"

if [ ! -S "$SLINIT_SOCK" ]; then
    _TESTS_RUN=$((_TESTS_RUN + 1))
    echo "SKIP: $SLINIT_SOCK not a socket (target daemon not running?)"
    test_summary
    exit 0
fi

svc_deploy "$SVC_SOCK" <<EOF
type = scripted
condition-path-is-socket = $SLINIT_SOCK
command = /bin/sh -c 'touch $MARK_SOCK; exit 0'
restart = false
EOF

svc_deploy "$SVC_REG" <<EOF
type = scripted
condition-path-is-socket = $NOT_A_SOCK
command = /bin/sh -c 'touch $MARK_REG; exit 0'
restart = false
EOF

svc_deploy "$SVC_MISS" <<EOF
type = scripted
condition-path-is-socket = $MISSING
command = /bin/sh -c 'touch $MARK_MISS; exit 0'
restart = false
EOF

slinitctl --system start "$SVC_SOCK" >/dev/null 2>&1
slinitctl --system start "$SVC_REG" >/dev/null 2>&1
slinitctl --system start "$SVC_MISS" >/dev/null 2>&1
wait_for_service "$SVC_SOCK" "STARTED" 10 || true
wait_for_service "$SVC_REG" "STARTED" 10 || true
wait_for_service "$SVC_MISS" "STARTED" 10 || true

assert_service_state "$SVC_SOCK" "STARTED" "socket-target reaches STARTED"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MARK_SOCK" ]; then
    echo "OK: S_ISSOCK true on $SLINIT_SOCK — command ran"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: expected socket predicate to succeed on $SLINIT_SOCK"
fi

assert_service_state "$SVC_REG" "STARTED" "regular-file target reaches STARTED"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$MARK_REG" ]; then
    echo "OK: regular file $NOT_A_SOCK skipped (not S_ISSOCK)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: regular file incorrectly satisfied path-is-socket"
fi

assert_service_state "$SVC_MISS" "STARTED" "missing-path target reaches STARTED"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$MARK_MISS" ]; then
    echo "OK: missing path $MISSING skipped"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: missing path incorrectly satisfied path-is-socket"
fi

test_summary
