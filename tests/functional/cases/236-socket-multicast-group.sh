#!/bin/sh
# 236-socket-multicast-group — the membership really reaches the kernel,
# is absent without the directive, and is released on stop.
#
# TWO THINGS THIS CASE HAD TO LEARN ABOUT THE VM.
#
# The image has only `lo` and NO routes at all. IP_ADD_MEMBERSHIP with
# INADDR_ANY makes the kernel choose an interface from the routing table,
# so with no route the join fails and the service correctly refuses to
# start. The case therefore adds a multicast route on lo itself, rather
# than skipping: same service, with the route it starts and without it it
# does not, which is also the comparison that proved the cause.
#
# And `ip maddr show` does NOT list the group even once the join has
# succeeded on lo. /proc/net/igmp is the authority. It prints the group
# as little-endian hex on x86, so 239.255.42.99 reads as 632AFFEF — not
# the EFFF2A63 the dotted quad suggests.

GROUP=239.255.42.99
GROUP_HEX=632AFFEF

joined() {
    grep -qi "$GROUP_HEX" /proc/net/igmp 2>/dev/null
}

ip route add 224.0.0.0/4 dev lo 2>/dev/null
echo "INFO: routes: [$(ip route show 2>/dev/null | tr '\n' ';')]"

_TESTS_RUN=$((_TESTS_RUN + 1))
if joined; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $GROUP is already joined before the test — it would prove nothing"
else
    echo "OK: baseline — $GROUP not joined"
fi

# Negative control. It asserts the service STARTED as well: without that,
# "no group joined" is satisfied by a service that never ran, and the
# control would be vacuous. An earlier version of this case had that
# hole.
cat > /etc/slinit.d/test-mcast-off <<'EOF'
type = process
command = /bin/sleep 30
socket-listen = udp:0.0.0.0:5399
EOF
slinitctl --system start test-mcast-off > /dev/null 2>&1
_off_state=$(slinitctl --system status test-mcast-off 2>/dev/null | awk '/^ *State:/ { print $2; exit }')
_off_joined=no
joined && _off_joined=yes
echo "INFO: without the directive: state=$_off_state joined=$_off_joined"
slinitctl --system --ignore-unstarted stop test-mcast-off > /dev/null 2>&1
slinitctl --system unload test-mcast-off > /dev/null 2>&1
rm -f /etc/slinit.d/test-mcast-off

assert_eq "$_off_state" "STARTED" "the control service starts (so its result means something)"
assert_eq "$_off_joined" "no" "a udp listener alone joins no group"

# The directive itself.
cat > /etc/slinit.d/test-mcast-on <<EOF
type = process
command = /bin/sleep 30
socket-listen = udp:0.0.0.0:5399
socket-multicast-group = $GROUP
EOF
slinitctl --system start test-mcast-on > /dev/null 2>&1
_on_state=$(slinitctl --system status test-mcast-on 2>/dev/null | awk '/^ *State:/ { print $2; exit }')
_on_joined=no
joined && _on_joined=yes
echo "INFO: with the directive: state=$_on_state joined=$_on_joined"

assert_eq "$_on_state" "STARTED" "the service with socket-multicast-group starts"
assert_eq "$_on_joined" "yes" "/proc/net/igmp shows the group joined"

slinitctl --system --ignore-unstarted stop test-mcast-on > /dev/null 2>&1
slinitctl --system unload test-mcast-on > /dev/null 2>&1
rm -f /etc/slinit.d/test-mcast-on

# The membership belongs to the socket, so stopping must release it.
# A leaked membership is otherwise invisible.
sleep 1
_after=no
joined && _after=yes
assert_eq "$_after" "no" "the membership is released when the service stops"

# A tcp listener cannot carry a membership, so this must be refused at
# load rather than stored and never used. The reason is asserted through
# slinit-check: `slinitctl start` reports the generic "could not be
# loaded" for every load error, so it cannot show which directive was
# at fault.
cat > /etc/slinit.d/test-mcast-tcp <<EOF
type = process
command = /bin/sleep 30
socket-listen = tcp:0.0.0.0:5399
socket-multicast-group = $GROUP
EOF
_lint=$(slinit-check -d /etc/slinit.d test-mcast-tcp 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_lint" | grep -q "socket-multicast-group requires a udp"; then
    echo "OK: a tcp listener is refused, and the reason names the directive"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: tcp listener not refused by name: $_lint"
fi

_start=$(slinitctl --system start test-mcast-tcp 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_start" | grep -qi "could not be loaded\|failed"; then
    echo "OK: the daemon also refuses it"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: the daemon accepted a tcp listener with a multicast group: $_start"
fi
slinitctl --system --ignore-unstarted stop test-mcast-tcp > /dev/null 2>&1
slinitctl --system unload test-mcast-tcp > /dev/null 2>&1
rm -f /etc/slinit.d/test-mcast-tcp

ip route del 224.0.0.0/4 dev lo 2>/dev/null

test_summary
