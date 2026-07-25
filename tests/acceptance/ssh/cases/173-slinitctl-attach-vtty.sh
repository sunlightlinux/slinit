#!/bin/sh
# 173-slinitctl-attach-vtty — `slinitctl attach <svc>` opens a client
# connection to the service's virtual-TTY multiplexer. The full
# interactive detach dance needs a real terminal, so this case exercises
# the plumbing: vtty svc reaches STARTED, its socket exists at the
# expected location, and `attach` is available in the CLI surface.

SVC="acceptance-test-vtty"

cleanup() { svc_remove "$SVC"; }
trap cleanup EXIT INT TERM

svc_deploy "$SVC" <<EOF
type = process
vtty = true
vtty-scrollback = 4096
command = /bin/sh -c 'while true; do echo tick; sleep 5; done'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null
wait_for_service "$SVC" "STARTED" 10 || { test_summary; return; }

# The vtty socket lives under /run/slinit/vtty/<svc>.sock (or the
# distro-preferred location). We check for any socket file matching
# the svc name so we don't hard-code the wrong path.
_socks=$(find /run/slinit -name "*${SVC}*" 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_socks" ]; then
    echo "OK: vtty socket present at: $_socks"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no vtty socket found under /run/slinit for $SVC"
fi

# `slinitctl attach` in non-tty mode should still recognise the svc
# and open the socket. We invoke it with input redirected from
# /dev/null (immediate EOF); the client should exit gracefully.
timeout 3 slinitctl --system attach "$SVC" </dev/null >/dev/null 2>&1
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
# 0 (clean exit) or 124 (timeout) both mean "the client connected".
# 1 or higher generally means "svc not found / no vtty".
if [ "$_rc" -eq 0 ] || [ "$_rc" -eq 124 ]; then
    echo "OK: attach client connected (rc=$_rc)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: attach client failed (rc=$_rc)"
fi

test_summary
