#!/bin/sh
# assert.sh - Test assertion helpers for guest-side test scripts.
# Sourced automatically by the guest runner.

_TESTS_RUN=0
_TESTS_FAILED=0

# assert_eq VALUE EXPECTED [MESSAGE]
# Fails if VALUE != EXPECTED.
assert_eq() {
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if [ "$1" != "$2" ]; then
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: ${3:-assert_eq}: got '$1', expected '$2'"
        return 1
    fi
    echo "OK: ${3:-assert_eq '$1' == '$2'}"
    return 0
}

# assert_contains HAYSTACK NEEDLE [MESSAGE]
# Fails if HAYSTACK does not contain NEEDLE.
assert_contains() {
    _TESTS_RUN=$((_TESTS_RUN + 1))
    case "$1" in
        *"$2"*) ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: ${3:-assert_contains}: '$2' not found in output"
            return 1
            ;;
    esac
    echo "OK: ${3:-assert_contains '$2'}"
    return 0
}

# assert_not_contains HAYSTACK NEEDLE [MESSAGE]
assert_not_contains() {
    _TESTS_RUN=$((_TESTS_RUN + 1))
    case "$1" in
        *"$2"*)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: ${3:-assert_not_contains}: '$2' unexpectedly found in output"
            return 1
            ;;
    esac
    echo "OK: ${3:-assert_not_contains '$2'}"
    return 0
}

# assert_exit_code COMMAND EXPECTED_CODE [MESSAGE]
# Runs COMMAND and checks exit code.
assert_exit_code() {
    _TESTS_RUN=$((_TESTS_RUN + 1))
    eval "$1" >/dev/null 2>&1
    _rc=$?
    if [ "$_rc" != "$2" ]; then
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: ${3:-assert_exit_code}: '$1' exited $_rc, expected $2"
        return 1
    fi
    echo "OK: ${3:-exit code $2 for '$1'}"
    return 0
}

# assert_service_state SERVICE EXPECTED_STATE [MESSAGE]
# Checks service state via slinitctl is-started / status.
assert_service_state() {
    _TESTS_RUN=$((_TESTS_RUN + 1))
    _state=$(slinitctl --system status "$1" 2>/dev/null | grep 'State:' | awk '{print $2}')
    if [ "$_state" != "$2" ]; then
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: ${3:-service '$1' state}: got '$_state', expected '$2'"
        return 1
    fi
    echo "OK: ${3:-service '$1' is $2}"
    return 0
}

# wait_for_service SERVICE STATE [TIMEOUT_SEC]
# Polls until service reaches the given state or timeout.
wait_for_service() {
    _svc="$1"
    _want="$2"
    _timeout="${3:-10}"
    _elapsed=0
    while [ "$_elapsed" -lt "$_timeout" ]; do
        _cur=$(slinitctl --system status "$_svc" 2>/dev/null | grep 'State:' | awk '{print $2}')
        if [ "$_cur" = "$_want" ]; then
            return 0
        fi
        sleep 1
        _elapsed=$((_elapsed + 1))
    done
    echo "TIMEOUT: service '$_svc' did not reach '$_want' within ${_timeout}s (current: $_cur)"
    return 1
}

# test_summary - prints summary and returns appropriate exit code.
test_summary() {
    echo ""
    echo "--- Results: $_TESTS_RUN run, $_TESTS_FAILED failed ---"
    if [ "$_TESTS_FAILED" -gt 0 ]; then
        return 1
    fi
    return 0
}
