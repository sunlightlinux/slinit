#!/bin/sh
# 194-pass-cs-fd-child — `options = pass-cs-fd` hands the child process
# a live control-socket file descriptor. The child reads
# $SLINIT_CS_FD from its env and can talk directly to the daemon
# without opening its own socket. Verify: env var present with a
# numeric fd, and the fd points at a socket.

SVC="acceptance-test-cs-fd"
LOG=/tmp/acceptance-cs-fd.log

cleanup() { svc_remove "$SVC"; rm -f "$LOG"; }
trap cleanup EXIT INT TERM
rm -f "$LOG"

svc_deploy "$SVC" <<EOF
type = process
options = pass-cs-fd
# Runtime env refs must use \$\$VAR in the heredoc — slinit's config
# parser pre-expands \$VAR at load time, so a bare \$SLINIT_CS_FD
# would collapse to empty. Doubled dollar defers the reference to
# the runtime shell where SLINIT_CS_FD is actually set.
command = /bin/sh -c "env | grep SLINIT_CS_FD > $LOG; _fd=\$\$SLINIT_CS_FD; [ -n \"\$\$_fd\" ] && readlink /proc/self/fd/\$\$_fd >> $LOG 2>&1; exec sleep 600"
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null
wait_for_service "$SVC" "STARTED" 10 || { test_summary; return; }
sleep 1

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$LOG" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $LOG missing — child body did not run"
    test_summary
    return
fi

_env_line=$(grep '^SLINIT_CS_FD=' "$LOG")
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_env_line" ]; then
    echo "OK: $_env_line present in child env"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: SLINIT_CS_FD not in child env — pass-cs-fd didn't fire"
    test_summary
    return
fi

# The readlink line should show a socket path (or "socket:[N]" style).
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -qE 'socket:|slinit' "$LOG"; then
    echo "OK: SLINIT_CS_FD points at a live socket"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: fd target doesn't look like a socket; dump:"
    sed 's/^/    /' "$LOG"
fi

test_summary
