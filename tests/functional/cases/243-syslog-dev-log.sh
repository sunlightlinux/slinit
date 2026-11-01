#!/bin/sh
# 243-syslog-dev-log — PID 1 serves /dev/log into the journal.
#
# A system with no syslog daemon used to have no /dev/log at all, so
# syslog(3) — sudo, login, su, dhcpcd — discarded every message. slinit
# now binds it in system mode and emits each message with
# Transport=syslog. Checks: the socket exists and every user may write
# to it; a logger(1) line is found by its tag; its priority comes from
# <PRI>; and the sender's identity is the kernel's (SCM_CREDENTIALS),
# not PID 1's — an unprivileged sender shows its own uid.

wait_for_service "boot" "STARTED" 15

command -v logger >/dev/null 2>&1 || skip_case "no logger(1) in the guest"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -S /dev/log ] && [ "$(stat -c %a /dev/log)" = "666" ]; then
    echo "OK: /dev/log is a socket, mode 666"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: /dev/log missing or not mode 666: $(ls -la /dev/log 2>&1)"
fi

_tag="sl243_$$"
logger -t "$_tag" -p user.err "hello through syslog"

# The reader emits as soon as the datagram arrives; allow a moment.
_out=
_n=0
while [ $_n -lt 20 ]; do
    _out=$(slinit-journalctl -t "$_tag" -o json -n 5 2>/dev/null)
    [ -n "$_out" ] && break
    sleep 0.1
    _n=$((_n + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_out" | grep -q '"msg":"hello through syslog"'; then
    echo "OK: logger line found by its tag"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: logger line not in the journal: '$_out'"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_out" | grep -q '"_transport":"syslog"' && echo "$_out" | grep -q '"prio":3'; then
    echo "OK: transport syslog, priority err from <PRI>"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: wrong transport or priority: $_out"
fi

# PID 1 must not stamp itself as the sender.
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_out" | grep -q '"_pid":1,'; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: event claims PID 1 sent it: $_out"
else
    echo "OK: sender pid is not PID 1's"
fi

# An unprivileged user can log, and is recorded as itself.
_tag2="sl243u_$$"
if su nobody -s /bin/sh -c "logger -t $_tag2 from-nobody" 2>/dev/null; then
    sleep 0.3
    _out2=$(slinit-journalctl -t "$_tag2" -o json -n 1 2>/dev/null)
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if echo "$_out2" | grep -q "\"_uid\":$(id -u nobody)"; then
        echo "OK: unprivileged sender recorded with its own uid"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: unprivileged line missing or wrong uid: '$_out2'"
    fi
else
    echo "NOTE: su nobody unavailable; unprivileged check not run"
fi

test_summary
