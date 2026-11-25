#!/bin/sh
# 261-chroot-non-process-refused: chroot is only applied to type = process.
# On a scripted service it was silently ignored, so a command configured into
# a jail ran against the real root. It is now refused at load.

rm -f /run/261.ran
out=$(slinitctl --system start jailscript 2>&1)
rc=$?
echo "start: rc=$rc $out"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$rc" -ne 0 ]; then
    echo "OK: scripted service with chroot refused"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: scripted service with chroot was started"
fi
sleep 1
assert_eq "$(test -e /run/261.ran && echo ran || echo not-run)" "not-run" "its command did not run outside the jail"

test_summary
