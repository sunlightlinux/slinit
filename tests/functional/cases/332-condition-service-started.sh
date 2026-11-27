#!/bin/sh
# Test: condition-service-started / assert-service-started, slinit's
# Requisite=. Neither had a case.
#
# The named service must already be STARTED; the one naming it does not
# bring it up. With it down, the condition- service is skipped (its
# command never runs; a skipped service still reads STARTED, so a marker
# file is the evidence) and the assert- service fails to start. With it
# up, both run.

M=/run/332
rm -rf "$M"; mkdir -p "$M"
printf '%s\n' 'type = process' 'command = /bin/sleep 1000' > /etc/slinit.d/test-332-req
printf '%s\n' 'type = process' \
    "command = /bin/sh -c \"touch $M/cond; exec sleep 1000\"" \
    'condition-service-started = test-332-req' > /etc/slinit.d/test-332-cond
printf '%s\n' 'type = process' \
    "command = /bin/sh -c \"touch $M/assert; exec sleep 1000\"" \
    'assert-service-started = test-332-req' > /etc/slinit.d/test-332-assert

# The required service down.
timeout 10 slinitctl --system start test-332-cond >/dev/null 2>&1
sleep 0.5
[ ! -e "$M/cond" ]
assert_eq "$?" "0" "required service down: the condition- service is skipped (its command did not run)"
timeout 10 slinitctl --system start test-332-assert >/dev/null 2>&1
_rc=$?
[ "$_rc" != "0" ] && [ ! -e "$M/assert" ]
assert_eq "$?" "0" "required service down: the assert- service fails to start (rc $_rc)"
assert_service_state test-332-req "STOPPED" "neither started the required service"

slinitctl --system stop test-332-cond >/dev/null 2>&1
slinitctl --system stop test-332-assert >/dev/null 2>&1

# The required service up.
timeout 10 slinitctl --system start test-332-req >/dev/null 2>&1
timeout 10 slinitctl --system start test-332-cond >/dev/null 2>&1
timeout 10 slinitctl --system start test-332-assert >/dev/null 2>&1
sleep 0.5
[ -e "$M/cond" ]
assert_eq "$?" "0" "required service up: the condition- service runs"
[ -e "$M/assert" ]
assert_eq "$?" "0" "required service up: the assert- service runs"
assert_service_state test-332-assert "STARTED" "required service up: the assert- service is STARTED"

for s in cond assert req; do slinitctl --system stop test-332-$s >/dev/null 2>&1; done
test_summary
