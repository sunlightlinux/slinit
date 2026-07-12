#!/bin/sh
# Test: `slinitctl status` prints a File: line with the resolved
# description path; a mtime bump after load appends "(modified since
# loaded)". Plus the wire-format uint16 name-length cap: names
# starting with '.' (as a proxy — 65k names are hard to write) get
# rejected at load.

SVC="test-statusfile"
SVC_FILE="/etc/slinit.d/$SVC"

cat > "$SVC_FILE" <<'EOF'
type = process
command = /bin/sleep 60
restart = false
EOF
slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED"

_status=$(slinitctl --system status "$SVC" 2>&1)
assert_contains "$_status" "File:" "status output has 'File:' line"
assert_contains "$_status" "$SVC_FILE" \
    "File: line contains the description path"
assert_not_contains "$_status" "modified since loaded" \
    "no false 'modified' suffix on fresh service"

sleep 1
touch "$SVC_FILE"
sleep 1
_status=$(slinitctl --system status "$SVC" 2>&1)
assert_contains "$_status" "modified since loaded" \
    "touched file surfaces the 'modified since loaded' suffix"

# Name validation surface — '.' prefix is trivial to reproduce and
# proves the validator hook runs at load time.
_out=$(slinitctl --system load .startswithdot 2>&1)
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -ne 0 ]; then
    echo "OK: name starting with '.' rejected (rc=$_rc)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: '.' prefix name unexpectedly accepted"
fi

test_summary
