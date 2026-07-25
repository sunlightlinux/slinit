#!/bin/sh
# Test: full service-directory cluster — runtime/state/cache/logs/
# configuration each with -mode (+ -quota + -accounting where the
# directive exists).
# Validates: parser+loader accept every knob; the five directories
# are created with the requested mode. Quota/accounting are best-
# effort on kernels without cgroup memory tracking for the dir — no
# hard failure expected either way.

wait_for_service "dirs-svc" "STARTED" 10
assert_service_state "dirs-svc" "STARTED" "service-dir cluster parses + service starts"

# Spot-check each of the 5 dirs and its mode.
check_dir() {
    _label="$1"; _path="$2"; _want_mode="$3"
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if [ ! -d "$_path" ]; then
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: $_label $_path was not created"
        return
    fi
    _got=$(stat -c '%a' "$_path" 2>/dev/null)
    if [ "$_got" = "$_want_mode" ]; then
        echo "OK: $_label $_path created with mode $_want_mode"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: $_label $_path mode is $_got, expected $_want_mode"
    fi
}

check_dir "runtime-directory" /run/186-run 750
check_dir "state-directory"   /var/lib/186-state 755
check_dir "cache-directory"   /var/cache/186-cache 700
check_dir "logs-directory"    /var/log/186-logs 755
check_dir "configuration-directory" /etc/186-conf 700

test_summary
