#!/bin/sh
# Test: `slinitctl reload` of a stopped service drops a removed logfile.
#
# A reload of a stopped service of the same type updates its record in
# place. Up to v4.0.3 the log settings were only ever set, never cleared:
# a service whose file lost `logfile =` (or `stderr-logfile =`) kept
# writing to the old file until slinit itself restarted (04d2bf9).

L=/run/307.log
E=/run/307.err
rm -f "$L" "$E" /run/307.ran
printf '%s\n' 'type = process' \
    'command = /bin/sh -c "echo out-before-307; echo err-before-307 >&2; exec sleep 600"' \
    "logfile = $L" "stderr-logfile = $E" 'restart = false' > /etc/slinit.d/test-307

slinitctl --system start test-307 >/dev/null 2>&1
wait_for_service test-307 STARTED 10
sleep 1
grep -q out-before-307 "$L" 2>/dev/null || skip_case "logfile never received the first run's output"
grep -q err-before-307 "$E" 2>/dev/null || skip_case "stderr-logfile never received the first run's stderr"

slinitctl --system stop test-307 >/dev/null 2>&1
wait_for_service test-307 STOPPED 10

# Same type, both log keys gone.
printf '%s\n' 'type = process' \
    'command = /bin/sh -c "touch /run/307.ran; echo out-after-307; echo err-after-307 >&2; exec sleep 600"' \
    'restart = false' > /etc/slinit.d/test-307
_out=$(timeout 10 slinitctl --system reload test-307 2>&1)
echo "reload: $_out"

slinitctl --system start test-307 >/dev/null 2>&1
wait_for_service test-307 STARTED 10
sleep 1
# The reload took: it is the new command that ran (else the checks below
# would pass on the old one's output).
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e /run/307.ran ]; then
    echo "OK: the reloaded description is the one that ran"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: the reloaded description did not run"
fi
_l=$(cat "$L" 2>/dev/null)
_e=$(cat "$E" 2>/dev/null)
assert_not_contains "$_l" "out-after-307" "stdout no longer goes to the removed logfile"
assert_not_contains "$_e" "err-after-307" "stderr no longer goes to the removed stderr-logfile"

slinitctl --system stop test-307 >/dev/null 2>&1
test_summary
