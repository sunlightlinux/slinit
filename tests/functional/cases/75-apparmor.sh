#!/bin/sh
# Test: AppArmor confinement wiring + fail-closed behavior.
# The test kernel has no AppArmor LSM / no loaded profile, so a
# service requesting apparmor-switch must FAIL to start (slinit-runner
# cannot perform aa_change_onexec and aborts rather than exec'ing the
# program unconfined). A service without AppArmor stanzas is unaffected.
# This exercises the full path: parser -> ServiceRecord -> ExecParams
# -> needsRunnerWrap -> slinit-runner --apparmor -> changeOnExec error.

# Control: a plain service still starts normally.
wait_for_service "aa-control" "STARTED" 10
assert_service_state "aa-control" "STARTED" "non-confined service unaffected"

# The confined service must fail closed (never reach STARTED).
slinitctl --system start aa-confined >/dev/null 2>&1 || true
sleep 3
state=$(slinitctl --system status aa-confined 2>&1)
assert_not_contains "$state" "STARTED" "apparmor-switch service fails closed without AppArmor"

test_summary
