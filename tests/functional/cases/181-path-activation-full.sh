#!/bin/sh
# Test: full path-activation cluster — start-on-path-changed /
# start-on-path-modified / start-on-directory-not-empty.
# start-on-path-exists is covered by 72-path-activation.sh.
#
# Semantics under test: pkg/pathwatch arm + fire on each variant.
#
# Trigger paths live under /etc/slinit.d/ because pathwatch stats them
# at arm time (during service load, before any scripted svc can run).
# Only pre-existing paths survive that check; the .d/ overlay is our
# only pre-slinit staging surface, so the marker files ship there.

# Progress markers, written to the console so they land in the captured log.
#
# This case has twice failed in CI with "no result received" — a hang, not a
# failed assertion: the result file came back zero bytes, so the summary was
# never reached, and the console stopped after pchanged-svc with
# pmodified-svc never started. It wedged once locally too, sitting idle for
# twelve minutes, and has passed every attempt since — including three in a
# row under software emulation, which is how CI runs it.
#
# Since it cannot be reproduced on demand, the next occurrence has to
# explain itself. Each marker names the step about to run, so the console
# says which line stopped. No amount of reading the script establishes that.
mark() { echo "MARK: $1" > /dev/console 2>/dev/null || true; }

# --- Sub-case A: start-on-path-changed ---------------------------------
# Trigger by IN_CLOSE_WRITE — busybox's `>>` open+write+close does the job.
mark "A: poke 181-target-changed"
echo poke >> /etc/slinit.d/181-target-changed
mark "A: wait pchanged-svc"
wait_for_service "pchanged-svc" "STARTED" 10
assert_service_state "pchanged-svc" "STARTED" "start-on-path-changed fires on modify"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f /tmp/181-changed-fired ]; then
    echo "OK: pchanged-svc body ran"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: pchanged-svc marker missing"
fi

# --- Sub-case B: start-on-path-modified --------------------------------
mark "B: poke 181-target-modified"
echo poke >> /etc/slinit.d/181-target-modified
mark "B: wait pmodified-svc"
wait_for_service "pmodified-svc" "STARTED" 10
assert_service_state "pmodified-svc" "STARTED" "start-on-path-modified fires on modify"

# --- Sub-case C: start-on-directory-not-empty --------------------------
# The .keep file inside 181-dir-target/ makes it non-empty at arm time,
# so pathwatch.arm() short-circuits to fire immediately. No runtime
# trigger needed — just wait for the auto-start.
mark "C: wait pdirne-svc"
wait_for_service "pdirne-svc" "STARTED" 10
assert_service_state "pdirne-svc" "STARTED" "start-on-directory-not-empty fires at arm on non-empty dir"

mark "done: summary"
test_summary
