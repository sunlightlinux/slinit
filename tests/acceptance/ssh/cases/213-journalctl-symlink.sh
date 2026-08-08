#!/bin/sh
# 213-journalctl-symlink — v2.1.0 shipped a `journalctl → slinit-
# journalctl` symlink so systemd-muscle-memory scripts work
# unchanged. The binary does no argv[0] dispatch (per
# slpkgs/srcpkgs/slinit/template post_install comment); either
# name invokes the same parser.

# Both names must resolve.
_TESTS_RUN=$((_TESTS_RUN + 1))
if command -v journalctl >/dev/null; then
    echo "OK: journalctl is on PATH"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: journalctl not on PATH"
    test_summary
    return
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if command -v slinit-journalctl >/dev/null; then
    echo "OK: slinit-journalctl is on PATH"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinit-journalctl not on PATH"
    test_summary
    return
fi

# journalctl must be a symlink pointing at slinit-journalctl.
_TESTS_RUN=$((_TESTS_RUN + 1))
_link=$(readlink "$(command -v journalctl)" 2>/dev/null)
case "$_link" in
*slinit-journalctl*)
    echo "OK: journalctl → $_link" ;;
*)
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: journalctl is not a symlink to slinit-journalctl (readlink: '$_link')" ;;
esac

# Both invocations return identical --version output.
_v1=$(journalctl --version 2>&1)
_v2=$(slinit-journalctl --version 2>&1)
assert_eq "$_v1" "$_v2" "both names print the same version"

# Real query via journalctl works (behavior identical to
# slinit-journalctl, both talk to the same control socket).
_out=$(journalctl -n 1 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_out" ] && ! echo "$_out" | grep -qi "error"; then
    echo "OK: journalctl -n 1 returned an event"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: journalctl -n 1 misbehaved: $_out"
fi

test_summary
