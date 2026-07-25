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

# --- Sub-case A: start-on-path-changed ---------------------------------
# Trigger by IN_CLOSE_WRITE — busybox's `>>` open+write+close does the job.
echo poke >> /etc/slinit.d/181-target-changed
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
echo poke >> /etc/slinit.d/181-target-modified
wait_for_service "pmodified-svc" "STARTED" 10
assert_service_state "pmodified-svc" "STARTED" "start-on-path-modified fires on modify"

# --- Sub-case C: start-on-directory-not-empty --------------------------
# The .keep file inside 181-dir-target/ makes it non-empty at arm time,
# so pathwatch.arm() short-circuits to fire immediately. No runtime
# trigger needed — just wait for the auto-start.
wait_for_service "pdirne-svc" "STARTED" 10
assert_service_state "pdirne-svc" "STARTED" "start-on-directory-not-empty fires at arm on non-empty dir"

test_summary
