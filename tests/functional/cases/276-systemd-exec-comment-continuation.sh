#!/bin/sh
# Test: a comment line inside a backslash-continued ExecStart= is
# skipped, as systemd skips it, for a .service unit PID 1 loads directly.
#
# The unit reader joined every line after one ending in a backslash, the
# comment line included, so the options an administrator had commented
# out ("#", "--debug", "--insecure") were passed to the daemon again.

slinitctl --system start argrec >/dev/null 2>&1
wait_for_service argrec STARTED 10 || true
assert_service_state argrec STARTED "the .service unit loads and starts"

_i=0
while [ ! -s /tmp/276-args ] && [ "$_i" -lt 10 ]; do sleep 0.5; _i=$((_i + 1)); done
echo "argv recorded:"; sed 's/^/  | /' /tmp/276-args 2>/dev/null

assert_eq "$(tr '\n' ' ' < /tmp/276-args 2>/dev/null)" "--first --last " \
    "only the uncommented arguments reach the process"
assert_not_contains "$(cat /tmp/276-args 2>/dev/null)" "--debug" \
    "the commented-out --debug is not passed"

slinitctl --system stop argrec >/dev/null 2>&1
test_summary
