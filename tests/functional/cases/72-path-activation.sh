#!/bin/sh
# Test: path-based activation (start-on-path-exists).
# Validates: pkg/pathwatch inotify watcher fires when the configured
# trigger file appears, slinit auto-starts the service via the
# OnServiceLoaded callback wiring in cmd/slinit/main.go. Rearm behavior
# is covered by the pkg/pathwatch unit test TestRearmFiresAgain — this
# test only exercises the production wiring end-to-end inside a real
# slinit-as-PID-1 VM.

# Trigger file does not exist at boot, so the service must remain STOPPED.
sleep 1
assert_service_state "path-svc" "STOPPED" "path-svc is STOPPED before trigger"

# Create the trigger file. inotify on /run should fire IN_CREATE on
# basename "slinit-path-trigger"; pathwatch dispatches to the
# registered callback, which calls serviceSet.StartService(path-svc).
: > /run/slinit-path-trigger

wait_for_service "path-svc" "STARTED" 10
assert_service_state "path-svc" "STARTED" "path-svc started on file create"

# Confirm the service body actually ran inside the child process.
assert_eq "$(cat /tmp/path-marker 2>/dev/null)" "fired" "service command executed"

test_summary
