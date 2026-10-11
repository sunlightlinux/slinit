#!/bin/sh
# 238-socket-accept-inetd — inetd `nowait` end to end: a real TCP
# connection must reach a per-connection instance, with the CONNECTED
# socket on stdio.
#
# The discriminating test is the echo. If the service were handed the
# listener instead (the default, `wait` mode), `cat` would read a
# listening socket and nothing would come back — so a returned payload
# proves the accept happened and the accepted fd, not the listener, is
# on stdin. Asserting only that the service STARTED would pass in both
# modes, which is the trap here.
#
# The VM has no network interfaces beyond `lo`, which is all this needs:
# the listener binds 127.0.0.1.

PORT=7799

cat > /etc/slinit.d/test-inetd-echo <<EOF
type = process
command = /bin/cat
socket-listen = tcp:127.0.0.1:$PORT
socket-accept = yes
EOF

slinitctl --system start test-inetd-echo > /dev/null 2>&1
_state=$(slinitctl --system status test-inetd-echo 2>/dev/null | awk '/^ *State:/ { print $2; exit }')
echo "INFO: state=$_state"
assert_eq "$_state" "STARTED" "the socket-accept service starts"

# The helper, not slinit, must own the listener.
_holder=$(slinitctl --system status test-inetd-echo 2>/dev/null | awk '/^ *PID:/ { print $2; exit }')
_cmd=$(cat /proc/${_holder:-0}/cmdline 2>/dev/null | tr '\0' ' ')
echo "INFO: supervised process $_holder: $_cmd"
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_cmd" | grep -q "slinit-inetd"; then
    echo "OK: the supervised process is the helper, so PID 1 is not in the accept path"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: the supervised process is not slinit-inetd: $_cmd"
fi

# The echo. This is the assertion that distinguishes nowait from wait.
_reply=$(printf 'ping\n' | timeout 10 nc 127.0.0.1 $PORT 2>/dev/null | head -1)
echo "INFO: reply=[$_reply]"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_reply" = "ping" ]; then
    echo "OK: the connected socket reached the instance on stdin and stdout"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: expected the payload echoed back, got [$_reply]"
fi

# A second connection must also be served: a one-shot accept would
# answer once and then go deaf, which the single check above cannot
# tell apart from a working loop.
_reply2=$(printf 'again\n' | timeout 10 nc 127.0.0.1 $PORT 2>/dev/null | head -1)
echo "INFO: reply2=[$_reply2]"
assert_eq "$_reply2" "again" "a second connection is served too"

# And the service is still up: per-connection instances exiting must not
# be mistaken for the service dying.
_state2=$(slinitctl --system status test-inetd-echo 2>/dev/null | awk '/^ *State:/ { print $2; exit }')
assert_eq "$_state2" "STARTED" "the service survives its instances exiting"

# No zombies: the helper reaps its own children. A per-connection model
# that leaks zombies would exhaust the pid table on a busy port.
_zombies=$(ps -o stat= -o comm= 2>/dev/null | awk '$1 ~ /^Z/ { print $2 }' | wc -l)
echo "INFO: zombies on the system: $_zombies"
assert_eq "$_zombies" "0" "the helper reaps its per-connection children"

slinitctl --system --ignore-unstarted stop test-inetd-echo > /dev/null 2>&1
slinitctl --system unload test-inetd-echo > /dev/null 2>&1
rm -f /etc/slinit.d/test-inetd-echo

# A udp listener has no accept, so this must be refused at load with the
# directive named, not accepted and quietly ineffective.
cat > /etc/slinit.d/test-inetd-udp <<EOF
type = process
command = /bin/cat
socket-listen = udp:127.0.0.1:$PORT
socket-accept = yes
EOF
_lint=$(slinit-check -d /etc/slinit.d test-inetd-udp 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_lint" | grep -q "socket-accept cannot be used with a udp"; then
    echo "OK: a udp listener is refused, and the reason names the directive"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: udp not refused by name: $_lint"
fi
slinitctl --system unload test-inetd-udp > /dev/null 2>&1
rm -f /etc/slinit.d/test-inetd-udp

test_summary
