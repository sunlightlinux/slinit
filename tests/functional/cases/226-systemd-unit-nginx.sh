#!/bin/sh
# Test: a stock systemd .service unit runs a real daemon under slinit.
# Validates: .service resolution from /etc/systemd/system, Type=forking ->
# bgprocess with the PID taken from PIDFile, RuntimeDirectory created on
# the /run tmpfs before ExecStart, PrivateTmp and NoNewPrivileges applied,
# ExecReload, and ExecStop.
#
# nginx rather than a shell stub on purpose: a stub that writes a pidfile
# and sleeps proves the translation but not that a real self-backgrounding
# daemon is tracked correctly, which is the part that breaks in practice.
#
# Nothing is converted to a file. /etc/slinit.d has no nginx description,
# and if one appeared it would win — so this case also fails if the
# fallback ordering regresses.
#
# KNOWN TO FAIL INTERMITTENTLY, roughly two runs in five, at the first
# assertion with state STOPPED and FAILEDSTART in the journal. This is a
# true positive, not a flaky test: pkg/service/bgprocess.go reads
# pid-file exactly once, immediately after the launcher exits, and a
# real forking daemon has not necessarily written it by then. Measured
# in this VM: the pidfile was absent at that instant in 7 of 10 nginx
# starts. dinit reads once too (proc-service.cc), so slinit is at parity
# rather than regressed; systemd polls. Left ungated on purpose — the
# case is reporting a defect that exists, and gating it would hide the
# very thing it was written to find. Fix is a bounded poll honouring
# start-timeout; this comment goes when that lands.

wait_for_service "nginx" "STARTED" 25
assert_service_state "nginx" "STARTED" "nginx from a .service unit is STARTED"

# It must come from the unit, not from a slinit description someone added.
assert_eq "$(test -e /etc/slinit.d/nginx && echo yes || echo no)" "no" \
    "no native description exists — the unit is what ran"
assert_eq "$(test -f /etc/systemd/system/nginx.service && echo yes || echo no)" "yes" \
    "the unit is still only a unit, nothing was generated"

# Description= is a directive, not a comment. It reached the service or
# it did not.
_desc=$(slinitctl --system status nginx 2>/dev/null | grep 'Description:' | cut -d: -f2- | sed 's/^ *//')
assert_eq "$_desc" "nginx test unit" "Description= reached the service"

# Type=forking -> bgprocess.
_type=$(slinitctl --system status nginx 2>/dev/null | grep 'Type:' | awk '{print $2}')
assert_eq "$_type" "bgprocess" "Type=forking became a bgprocess"

# RuntimeDirectory=nginx had to create /run/nginx before ExecStart: /run
# is a tmpfs, so a directory shipped in the image is gone by now. Without
# it nginx cannot write its pidfile and never reaches STARTED.
assert_eq "$(test -d /run/nginx && echo yes || echo no)" "yes" \
    "RuntimeDirectory created /run/nginx on the tmpfs"

# PIDFile= -> pid-file: slinit must track the daemon's own PID, not the
# PID of the process it forked from.
assert_eq "$(test -f /run/nginx/nginx.pid && echo yes || echo no)" "yes" \
    "nginx wrote its pidfile"
file_pid=$(cat /run/nginx/nginx.pid 2>/dev/null | tr -d '[:space:]')
status_pid=$(slinitctl --system status nginx 2>/dev/null | grep 'PID:' | awk '{print $2}')
assert_eq "$status_pid" "$file_pid" "slinit tracks the PID from PIDFile"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$file_pid" ] && kill -0 "$file_pid" 2>/dev/null; then
    echo "OK: nginx master $file_pid is alive"
else
    echo "FAIL: nginx master '$file_pid' is not running"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# The daemon has to actually work, not merely reach STARTED. A unit can
# translate cleanly and still produce a process that serves nothing.
_TESTS_RUN=$((_TESTS_RUN + 1))
if wget -q -O /tmp/nginx-index.html http://127.0.0.1/ 2>/dev/null &&
   grep -qi 'nginx' /tmp/nginx-index.html; then
    echo "OK: nginx served a page over HTTP"
else
    echo "FAIL: nginx did not serve a page (state says STARTED)"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# NoNewPrivileges=yes is an `options` member, not a setting of its own.
# Emitted the wrong way it makes the whole unit unloadable, so reaching
# this far already proves a lot; check the bit is set on the child too.
_TESTS_RUN=$((_TESTS_RUN + 1))
_nnp=$(awk '/^NoNewPrivs:/ {print $2}' "/proc/${file_pid}/status" 2>/dev/null)
if [ "$_nnp" = "1" ]; then
    echo "OK: NoNewPrivileges=yes reached the process (NoNewPrivs: 1)"
else
    echo "FAIL: NoNewPrivs is '${_nnp}', expected 1"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# PrivateTmp=yes gives the service its own /tmp. Write from outside and
# the service must not see it — comparing mount namespaces would pass
# even if the mount were shared.
_TESTS_RUN=$((_TESTS_RUN + 1))
_marker="/tmp/host-marker-$$"
: > "$_marker"
if [ -n "$file_pid" ] && [ -r "/proc/${file_pid}/root" ]; then
    if [ -e "/proc/${file_pid}/root${_marker}" ]; then
        echo "FAIL: PrivateTmp=yes but the service sees the host /tmp"
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
    else
        echo "OK: PrivateTmp=yes — the service has its own /tmp"
    fi
else
    echo "FAIL: cannot inspect /proc/${file_pid}/root"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi
rm -f "$_marker"

# ExecReload -> reload. nginx keeps the master PID across a reload, so a
# changed PID here means slinit restarted it instead of reloading.
slinitctl --system reload nginx >/dev/null 2>&1
sleep 2
_pid_after=$(cat /run/nginx/nginx.pid 2>/dev/null | tr -d '[:space:]')
assert_eq "$_pid_after" "$file_pid" "reload kept the master PID (ExecReload, not a restart)"
assert_service_state "nginx" "STARTED" "nginx still STARTED after reload"

# ExecStop -> stop-command. `nginx -s quit` is a graceful shutdown, so
# the master must be gone afterwards.
slinitctl --system stop nginx >/dev/null 2>&1
sleep 3
assert_service_state "nginx" "STOPPED" "nginx is STOPPED after stop"

_TESTS_RUN=$((_TESTS_RUN + 1))
if kill -0 "$file_pid" 2>/dev/null; then
    echo "FAIL: nginx master $file_pid survived the stop"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
else
    echo "OK: ExecStop reaped the nginx master"
fi

test_summary
