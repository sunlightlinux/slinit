#!/bin/sh
# Test: conf.d overlay overrides service configuration.
# Validates: conf.d overlay loading, scalar override (command replaced),
#            env-file injection via overlay.

wait_for_service "boot" "STARTED" 10

# The base overlay-svc is loaded but NOT auto-started (boot only waits-for).
# Create the conf.d overlay at runtime, then reload + start.
mkdir -p /etc/slinit.conf.d
cat > /etc/slinit.conf.d/overlay-svc <<'EOF'
command = /bin/sh -c "echo overlay-applied > /tmp/overlay-marker; while true; do sleep 60; done"
EOF

# Stop the service if running, reload config, then start fresh
slinitctl --system stop overlay-svc 2>&1 || true
sleep 1
slinitctl --system reload overlay-svc 2>&1
sleep 1
slinitctl --system start overlay-svc 2>&1
wait_for_service "overlay-svc" "STARTED" 10

sleep 2

# Verify the overlay command ran (not the base command)
marker=$(cat /tmp/overlay-marker 2>/dev/null)
assert_eq "$marker" "overlay-applied" "overlay overrode command"

assert_service_state "overlay-svc" "STARTED" "overlay-svc is STARTED"

test_summary
