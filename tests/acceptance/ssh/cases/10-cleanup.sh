#!/bin/sh
# 10-cleanup — final-pass guard. If previous cases tore down properly, no
# 'acceptance-test-*' artifacts should remain in /etc/slinit.d or in the
# daemon's loaded service list.

_leftover_files="$(ls "${ACCEPTANCE_SVCDIR}"/${ACCEPTANCE_NS_PREFIX}* 2>/dev/null || true)"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_leftover_files" ]; then
    echo "OK: no leftover ${ACCEPTANCE_NS_PREFIX}* in ${ACCEPTANCE_SVCDIR}"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: leftover service files:"
    echo "$_leftover_files" | sed 's/^/      /'
fi

_loaded="$(slinitctl --system list 2>/dev/null | grep -o "${ACCEPTANCE_NS_PREFIX}[a-z0-9-]*" || true)"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_loaded" ]; then
    echo "OK: no loaded ${ACCEPTANCE_NS_PREFIX}* in daemon"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: still-loaded services:"
    echo "$_loaded" | sort -u | sed 's/^/      /'
fi

test_summary
