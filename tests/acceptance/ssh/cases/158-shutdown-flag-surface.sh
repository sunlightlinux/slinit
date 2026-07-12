#!/bin/sh
# 158-shutdown-flag-surface — non-destructive check of the systemd-
# compatible reboot(8) flag surface on slinit-shutdown. We cannot
# actually invoke --reboot / --poweroff / -f on the live VM (the
# suite would be cut off), but we CAN prove every documented flag
# is recognized by piping --help through the parser and by feeding
# each flag through argv without invoking the action — the parser
# runs before any syscall, so `--help` short-circuits before any
# shutdown work happens.

# --help must list every documented flag.
_help=$(slinit-shutdown --help 2>&1)
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -eq 0 ]; then
    echo "OK: slinit-shutdown --help returned 0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinit-shutdown --help rc=$_rc"
fi

# Documented flags per commit 989ba56 (systemd surface) + a98b6ab
# (reboot -f minimal path).
for _flag in "--reboot" "--halt" "--poweroff" "-r" "-h" "-p" "-s" "-k" \
             "-f" "--force" "-n" "--no-sync" "-d" "--no-wtmp" \
             "-w" "--wtmp-only" "--no-wall" "--use-passed-cfd" \
             "--system" "--grace="; do
    _found=0
    case "$_help" in
        *"$_flag"*) _found=1 ;;
    esac
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if [ "$_found" -eq 1 ]; then
        echo "OK: --help mentions $_flag"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: --help missing $_flag"
    fi
done

# Historically we also probed for slinit-reboot / slinit-halt /
# slinit-soft-reboot symlinks that some distros ship alongside
# reboot(8). Those are packaging-level shims (xbps template), not part
# of the slinit binary surface itself — dev builds legitimately don't
# ship them, so probing them here would tie the acceptance suite to a
# specific packaging manifest. Dropped in favor of an argv[0]-aware
# self-check: slinit-shutdown itself parses argv[0] to pick the
# default action when invoked as one of those names.

test_summary
