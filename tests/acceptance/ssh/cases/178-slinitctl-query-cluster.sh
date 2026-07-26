#!/bin/sh
# 178-slinitctl-query-cluster — read-only queries that don't need a
# fixture service: query-name (the daemon's SLINIT_SERVICENAME view),
# query-load-mech (currently always "file"), service-dirs (config search
# paths), and boot-time (structured startup timing).

# --- query-name (takes a service argument) ----------------------------
_TESTS_RUN=$((_TESTS_RUN + 1))
_qn=$(slinitctl --system query-name boot 2>&1)
_rc=$?
if [ $_rc -eq 0 ]; then
    echo "OK: slinitctl query-name boot returned ('$_qn')"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinitctl query-name boot failed rc=$_rc: $_qn"
fi

# --- query-load-mech --------------------------------------------------
_TESTS_RUN=$((_TESTS_RUN + 1))
_qlm=$(slinitctl --system query-load-mech 2>&1)
if [ $? -eq 0 ]; then
    # Output format is "Loader type: N (name)" — assert on the label
    # prefix rather than a specific loader name that could change.
    assert_contains "$_qlm" "Loader type:" "query-load-mech emits 'Loader type:' header"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: query-load-mech failed: $_qlm"
fi

# --- service-dirs -----------------------------------------------------
_sd=$(slinitctl --system service-dirs 2>&1)
assert_contains "$_sd" "/etc/slinit.d" "service-dirs includes /etc/slinit.d"

# --- boot-time --------------------------------------------------------
_bt=$(slinitctl --system boot-time 2>&1)
assert_contains "$_bt" "Startup" "boot-time has a Startup summary"

test_summary
