#!/bin/sh
# Test: a control socket that could not be created at start can be
# recreated with SIGUSR1, and the first connection to it does not crash
# slinit.
#
# When the bind fails at start (on PID 1: /run with no free inode, which
# any local user could cause through /run/user), slinit goes on without
# its control socket, as designed, and SIGUSR1 reopens it. Up to v4.0.3
# the server's context was recorded only after a successful bind, so the
# first connection to the reopened socket dereferenced nil and slinit
# panicked; as PID 1, a kernel panic (7155dd6). Here the bind is made to
# fail with ENOTDIR (the socket path's directory is a regular file) on a
# nested `slinit --user`, which is then fixed and SIGUSR1'd.

R=/tmp/323
rm -rf "$R"; mkdir -p "$R/svcs"
printf '%s\n' 'type = internal' > "$R/svcs/boot"
: > "$R/d"                       # a file where the socket's directory should be

setsid /sbin/slinit --user --socket-path "$R/d/sock" --services-dir "$R/svcs" \
    --log-file "$R/log" </dev/null >/dev/null 2>"$R/stderr" &
NEST=$!
sleep 2
kill -0 "$NEST" 2>/dev/null || skip_case "slinit --user exits when its socket cannot be bound: $(tail -2 "$R/log")"
[ -S "$R/d/sock" ] && skip_case "the bind did not fail"
echo "started without a control socket: $(grep -i -m1 'socket' "$R/log")"

rm -f "$R/d"; mkdir "$R/d"
kill -USR1 "$NEST"
_e=0
until [ -S "$R/d/sock" ] || [ "$_e" -ge 20 ]; do sleep 0.2; _e=$((_e + 1)); done
[ -S "$R/d/sock" ] || skip_case "SIGUSR1 did not recreate the socket: $(tail -2 "$R/log")"

_out=$(timeout 10 slinitctl --socket-path "$R/d/sock" list 2>&1)
_rc=$?
sleep 1
echo "first connection: rc=$_rc"
_TESTS_RUN=$((_TESTS_RUN + 1))
if kill -0 "$NEST" 2>/dev/null; then
    echo "OK: slinit survived the first connection to the reopened socket"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinit died on the first connection: $(grep -m1 -E 'panic|nil pointer' "$R/stderr")"
fi
assert_eq "$_rc" "0" "and answered it"
assert_contains "$_out" "boot" "with the service list"

kill -9 "$NEST" 2>/dev/null
test_summary
