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
#
# This used to call `slinitctl load`, which is not a verb: slinitctl
# exited 2 with a usage error before reaching the daemon, so the
# "rejected" it saw was its own. Now a real, valid description file
# sits under the dotted name and `start` asks the daemon to load it.
# The validator must refuse the NAME: the call fails with a daemon-side
# error (not a usage error, rc 2) and the file's command never runs —
# without the check, the file loads and the marker appears.
BADNAME=".startswithdot"
BAD_MARKER="/run/107-dotname-ran"
rm -f "$BAD_MARKER"
cat > "/etc/slinit.d/$BADNAME" <<EOF
type = scripted
command = /bin/touch $BAD_MARKER
EOF
_out=$(timeout 10 slinitctl --system start "$BADNAME" 2>&1)
_rc=$?
echo "  diag: start $BADNAME -> rc=$_rc: $_out"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -ne 0 ] && [ "$_rc" -ne 2 ]; then
    echo "OK: name starting with '.' rejected by the daemon (rc=$_rc)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: '.' prefix name: rc=$_rc (0 = accepted, 2 = slinitctl usage error)"
fi
assert_not_contains "$_out" "Unknown command" "slinitctl recognised the verb"
assert_not_contains "$_out" "sage:" "no usage error — the request reached the daemon"
sleep 1
[ -e "$BAD_MARKER" ] && _ran=yes || _ran=no
assert_eq "$_ran" "no" "the dotted service's command never ran"
rm -f "/etc/slinit.d/$BADNAME"

test_summary
