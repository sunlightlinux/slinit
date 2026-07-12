#!/bin/sh
# Test: cache-directory + cache-directory-mode creates /var/cache/<svc>
# with the requested mode.

SVC="test-cachd"
DIR="/var/cache/$SVC"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
cache-directory = $SVC
cache-directory-mode = 0755
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -d "$DIR" ]; then
    echo "OK: $DIR exists"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $DIR not created"
fi

_mode=$(stat -c '%a' "$DIR" 2>/dev/null)
assert_eq "$_mode" "755" "cache-directory mode = 755"

test_summary
