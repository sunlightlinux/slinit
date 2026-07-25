#!/bin/sh
# 170-slinitctl-freeze-thaw — `freeze` writes `1` to the service's
# cgroup.freeze; `thaw` writes `0`. Unlike `pause` (SIGSTOP), the
# frozen state is opaque to the process and only reversible via the
# cgroup.freeze knob.
#
# Requires cgroup v2 + a configured `cgroup =` for the service; slinit
# refuses to freeze services without one.

SVC="acceptance-test-freeze"

cleanup() { svc_remove "$SVC"; }
trap cleanup EXIT INT TERM

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e /sys/fs/cgroup/cgroup.controllers ]; then
    echo "SKIP: cgroup v2 not present"
    test_summary
    return 0
fi
echo "OK: cgroup v2 hierarchy present"

svc_deploy "$SVC" <<EOF
type = process
cgroup = /slinit/acceptance-freeze
command = /bin/sh -c 'exec sleep 600'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null
wait_for_service "$SVC" "STARTED" 10 || { test_summary; return; }

# Locate the cgroup.freeze knob (slinit places the svc under its cgroup path).
CG=/sys/fs/cgroup/slinit/acceptance-freeze
if [ ! -e "$CG/cgroup.freeze" ]; then
    _TESTS_RUN=$((_TESTS_RUN + 1))
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $CG/cgroup.freeze missing — cgroup layout differs from expectation"
    test_summary
    return
fi

# Freeze the service.
slinitctl --system freeze "$SVC" >/dev/null
sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$(cat "$CG/cgroup.freeze" 2>/dev/null)" = "1" ]; then
    echo "OK: cgroup.freeze == 1 after slinitctl freeze"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: cgroup.freeze not 1 after freeze (got '$(cat "$CG/cgroup.freeze")')"
fi

# Thaw.
slinitctl --system thaw "$SVC" >/dev/null
sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$(cat "$CG/cgroup.freeze" 2>/dev/null)" = "0" ]; then
    echo "OK: cgroup.freeze == 0 after slinitctl thaw"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: cgroup.freeze not 0 after thaw"
fi

test_summary
