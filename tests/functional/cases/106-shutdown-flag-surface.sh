#!/bin/sh
# Test: --help lists every documented reboot(8)-compat flag of
# slinit-shutdown. Non-destructive: --help short-circuits before any
# shutdown work — no reboot syscall reached.

_help=$(slinit-shutdown --help 2>&1)
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -eq 0 ]; then
    echo "OK: slinit-shutdown --help returned 0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinit-shutdown --help rc=$_rc"
fi

for _flag in "--reboot" "--halt" "--poweroff" "-r" "-h" "-p" "-s" "-k" \
             "-f" "--force" "-n" "--no-sync" "-d" "--no-wtmp" \
             "-w" "--wtmp-only" "--no-wall" "--use-passed-cfd" \
             "--system" "--grace="; do
    _TESTS_RUN=$((_TESTS_RUN + 1))
    case "$_help" in
        *"$_flag"*) echo "OK: --help mentions $_flag" ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: --help missing $_flag" ;;
    esac
done

test_summary
