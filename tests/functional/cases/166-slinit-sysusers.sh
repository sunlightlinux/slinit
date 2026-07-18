#!/bin/sh
# Test: slinit-sysusers --dry-run parses u/g/m/r directives cleanly
# without actually calling useradd/groupadd (shadow-utils are
# absent in the busybox VM; a wet run would fail).
mkdir -p /tmp/sysusers-fixture
cat > /tmp/sysusers-fixture/probe.conf <<'CONF'
u testuser 4242 "Test User" /var/lib/testuser /sbin/nologin
g testgrp 4243
m testuser testgrp
r - 500-800
CONF

_out=$(slinit-sysusers --dirs=/tmp/sysusers-fixture --dry-run 2>&1)
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -eq 0 ]; then
    echo "OK: dry-run exited 0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: dry-run rc=$_rc: $_out"
fi
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_out" | grep -q "would u testuser"; then
    echo "OK: u directive parsed"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: u directive not parsed: $_out"
fi
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_out" | grep -q "would g testgrp"; then
    echo "OK: g directive parsed"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: g directive not parsed: $_out"
fi
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_out" | grep -q "would m testuser"; then
    echo "OK: m directive parsed"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: m directive not parsed: $_out"
fi

test_summary
