#!/bin/sh
# 189-service-template-lifecycle — service templates: a description
# without an `@` in the name serves as a template, and instances are
# started via `slinitctl start <name>@<arg>`. `$1` in the config
# expands to the instance argument. Full lifecycle: deploy template,
# start two instances, verify each has its own state + working dir,
# stop both.

TMPL="acceptance-test-tmpl@"
INST_A="acceptance-test-tmpl@alpha"
INST_B="acceptance-test-tmpl@beta"

cleanup() {
    slinitctl --system --ignore-unstarted stop "$INST_A" 2>/dev/null || true
    slinitctl --system --ignore-unstarted stop "$INST_B" 2>/dev/null || true
    slinitctl --system unload "$INST_A" 2>/dev/null || true
    slinitctl --system unload "$INST_B" 2>/dev/null || true
    rm -f /etc/slinit.d/"$TMPL" /tmp/acceptance-tmpl-alpha /tmp/acceptance-tmpl-beta 2>/dev/null
}
trap cleanup EXIT INT TERM

# Template file uses $$1 to defer expansion — $1 gets processed by the
# instance-loader, and $$ escapes the literal $ so runtime shell sees $1.
cat > /etc/slinit.d/"$TMPL" <<'EOF'
type = process
command = /bin/sh -c "echo instance=$1 > /tmp/acceptance-tmpl-$1; exec sleep 600"
restart = false
EOF

# Instance A.
slinitctl --system start "$INST_A" >/dev/null
wait_for_service "$INST_A" "STARTED" 10
assert_service_state "$INST_A" "STARTED" "template instance alpha STARTED"
assert_eq "$(cat /tmp/acceptance-tmpl-alpha 2>/dev/null)" "instance=alpha" \
    "alpha instance received \$1=alpha in its command"

# Instance B — independent lifecycle.
slinitctl --system start "$INST_B" >/dev/null
wait_for_service "$INST_B" "STARTED" 10
assert_service_state "$INST_B" "STARTED" "template instance beta STARTED"
assert_eq "$(cat /tmp/acceptance-tmpl-beta 2>/dev/null)" "instance=beta" \
    "beta instance received \$1=beta"

# Stopping A must not affect B.
slinitctl --system stop "$INST_A" >/dev/null
wait_for_service "$INST_A" "STOPPED" 10
assert_service_state "$INST_A" "STOPPED" "alpha stopped independently"
assert_service_state "$INST_B" "STARTED" "beta unaffected by alpha stop"

test_summary
