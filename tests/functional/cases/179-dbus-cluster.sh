#!/bin/sh
# Test: bus-name / bus-policy / bus-name-scope directives.
# Validates: parse + start under the dbus-optional design. Both
# scope=system and scope=session load cleanly and the services reach
# STARTED without a running dbus daemon (the VM has none). The
# auto-wired ready-check is only synthesised when dbus-send is
# installed — absent here — so the record just stores the directive
# and moves on.
#
# Semantics under test: pkg/config/loader.go dbusSendAvailable check
# + dbusReadyCheckCommand injection.

wait_for_service "dbus-svc" "STARTED" 10
wait_for_service "dbus-session-svc" "STARTED" 10
assert_service_state "dbus-svc" "STARTED" "bus-name (system scope) parses + service starts"
assert_service_state "dbus-session-svc" "STARTED" "bus-name-scope=session parses + service starts"

test_summary
