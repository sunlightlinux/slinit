#!/bin/sh
# Test: reload service configuration from disk.
# Validates: CmdReloadService re-reads the service file.

wait_for_service "reload-svc" "STARTED" 10
assert_service_state "reload-svc" "STARTED" "reload-svc initially STARTED"

# Stop the service first (reload works on stopped services)
slinitctl --system stop reload-svc
wait_for_service "reload-svc" "STOPPED" 10

# Modify the service file on disk (change the command)
cat > /etc/slinit.d/reload-svc <<'EOF'
type = process
command = /bin/sh -c "echo reloaded > /tmp/reload-marker; while true; do sleep 60; done"
depends-on: system-init
EOF

# Reload the service config
output=$(slinitctl --system reload reload-svc 2>&1)
assert_contains "$output" "reloaded" "reload command succeeded"

# Start it again with the new config
slinitctl --system start reload-svc
wait_for_service "reload-svc" "STARTED" 10
assert_service_state "reload-svc" "STARTED" "reload-svc STARTED after reload"

# Verify the new command ran
sleep 1
assert_eq "$(cat /tmp/reload-marker 2>/dev/null)" "reloaded" "new command ran after reload"

test_summary
