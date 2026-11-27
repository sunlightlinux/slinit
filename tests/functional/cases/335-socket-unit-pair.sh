#!/bin/sh
# Test: a systemd service's .socket unit is read with it (4.1).
#
# test-335.service's daemon only records the LISTEN_FDS it was handed;
# test-335.socket listens on /run/test-335/s.sock, a directory nothing
# created before: slinit creates it, opens the socket with the socket
# unit's SocketMode=, and passes it as fd 3. Before 4.1 the socket unit
# was not read and the daemon got LISTEN_FDS unset. A socket with
# Accept=yes and only its template loads as the name of the pair.

slinitctl --system start test-335 >/dev/null 2>&1
wait_for_service test-335 "STARTED" 10
assert_service_state test-335 "STARTED" "the service of the pair started"
sleep 1
assert_eq "$(cat /run/test-335.fds 2>/dev/null)" "1" "the daemon was handed one listening socket"
assert_exit_code "test -S /run/test-335/s.sock" 0 "the socket exists, in a directory slinit created"
assert_eq "$(stat -c %a /run/test-335/s.sock 2>/dev/null)" "660" "SocketMode=0660 applied"
slinitctl --system stop test-335 >/dev/null 2>&1

# test-335a.socket has Accept=yes and there is only the template
# test-335a@.service: the name test-335a is that pair, the template's
# command run once per connection with the connection on stdin/stdout.
slinitctl --system start test-335a >/dev/null 2>&1
wait_for_service test-335a "STARTED" 10
assert_service_state test-335a "STARTED" "the Accept=yes pair loaded as test-335a"
_r=$(printf 'one\n' | timeout 10 nc 127.0.0.1 7335 2>/dev/null | head -1)
assert_eq "$_r" "got one" "a connection ran the template's command"
_r=$(printf 'two\n' | timeout 10 nc 127.0.0.1 7335 2>/dev/null | head -1)
assert_eq "$_r" "got two" "and a second connection ran it again"
slinitctl --system stop test-335a >/dev/null 2>&1

test_summary
