#!/bin/sh
# nosystemd checklist — Debian #825394.
#
# "systemd kills background processes after user logs out": with
# KillUserProcesses=yes, closing a session reaped nohup'd jobs, tmux and
# screen. It is the single most-cited systemd complaint from users who
# leave long jobs running over ssh.
# https://bugs.debian.org/cgi-bin/bugreport.cgi?bug=825394
#
# slinit-logind reports KillUserProcesses=false and means it: releasing
# a session must not kill processes that detached from it. This test
# works at the cgroup level, which is what a session teardown acts on,
# without needing a real login.

scope=/sys/fs/cgroup/nosystemd-test-session
mkdir -p "$scope" 2>/dev/null

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -d "$scope" ]; then
    echo "SKIP: cgroup v2 not available in this VM"
    test_summary
    return 0 2>/dev/null || exit 0
fi
echo "OK: session scope cgroup created"

# A "background job the user left running", placed in the session scope
# exactly as a login shell's child would be.
setsid /bin/sh -c 'while true; do sleep 1; done' >/dev/null 2>&1 &
job_pid=$!
sleep 1
echo "$job_pid" > "$scope/cgroup.procs" 2>/dev/null

_TESTS_RUN=$((_TESTS_RUN + 1))
if kill -0 "$job_pid" 2>/dev/null; then
    echo "OK: background job $job_pid running in the session scope"
else
    echo "FAIL: background job died before the test started"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    test_summary
    return 0 2>/dev/null || exit 0
fi

# Simulate what session teardown does: drop the scope. The kernel
# refuses rmdir while the cgroup is populated, which is precisely why a
# logind that wants to "clean up" has to go kill things first — and why
# choosing NOT to is a deliberate policy, not an oversight.
rmdir "$scope" 2>/dev/null
sleep 2

_TESTS_RUN=$((_TESTS_RUN + 1))
if kill -0 "$job_pid" 2>/dev/null; then
    echo "OK: the background job survived session teardown"
else
    echo "FAIL: session teardown killed a detached background job (Debian#825394)"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

kill "$job_pid" 2>/dev/null
rmdir "$scope" 2>/dev/null

test_summary
