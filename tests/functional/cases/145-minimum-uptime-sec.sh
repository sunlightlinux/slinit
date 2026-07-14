#!/bin/sh
# Test: slinit --help exposes the --minimum-uptime-sec flag
# (systemd v261 MinimumUptimeSec= equivalent). End-to-end
# verification of the shutdown-delay would require triggering a
# reboot from inside the test VM — that kills the test harness
# itself and is not usable here. The flag-surface test guards
# against a silent removal or rename.

_help=$(slinit --help 2>&1)
_rc=$?

_TESTS_RUN=$((_TESTS_RUN + 1))
# slinit's flag.PrintDefaults may exit non-zero on --help depending
# on the Go flag package version. Accept either 0 or 2 (usage exit).
case "$_rc" in
    0|2) echo "OK: slinit --help returned $_rc" ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: slinit --help unexpected rc=$_rc" ;;
esac

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_help" in
    *"minimum-uptime-sec"*)
        echo "OK: --help documents --minimum-uptime-sec"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: --help missing --minimum-uptime-sec"
        ;;
esac

# Also confirm the boot-loop guard is referenced in the flag help
# so users don't have to grep the source to understand what it does.
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_help" in
    *"anti-boot-loop"*|*"boot-loop"*|*"boot loop"*)
        echo "OK: --help mentions boot-loop semantics"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: --help lacks boot-loop context for the flag"
        ;;
esac

test_summary
