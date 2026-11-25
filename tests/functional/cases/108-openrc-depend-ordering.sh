#!/bin/sh
# Test: OpenRC init.d depend() { after other } maps to advisory
# ordering (AfterOptional), not a hard dep: starting B alone must
# leave A stopped. Both services load via the init.d auto-detect path.
#
# slinit enables the init.d fallback only when /etc/init.d exists at
# boot, and the base VM has none, so this case skipped on every run.
# It now ships both scripts in its .d/initd/, which creates the
# directory before boot.

INITD=/etc/init.d
SVC_A="test-oorca"
SVC_B="test-oorcb"

# Ensure init.d dir exists BEFORE we write into it — the functional VM
# base image may not create it. slinit's initdauto-detect snapshots the
# directory at boot, so we also need to reload-all to re-scan.
mkdir -p "$INITD"

# Canary probe: if the loader isn't rescanning /etc/init.d we skip.
_CANARY="test-oorc-probe"
cat > "$INITD/$_CANARY" <<'EOF'
#!/sbin/openrc-run
depend() { :; }
start() { return 0; }
EOF
chmod 0755 "$INITD/$_CANARY"
slinitctl --system reload-all >/dev/null 2>&1
sleep 1
if ! slinitctl --system status "$_CANARY" >/dev/null 2>&1; then
    rm -f "$INITD/$_CANARY"
    skip_case "init.d fallback disabled at slinit boot"
fi
slinitctl --system unload "$_CANARY" 2>/dev/null || true
rm -f "$INITD/$_CANARY"

cat > "$INITD/$SVC_A" <<EOF
#!/sbin/openrc-run
depend() {
    :
}
start() {
    return 0
}
EOF
chmod 0755 "$INITD/$SVC_A"

cat > "$INITD/$SVC_B" <<EOF
#!/sbin/openrc-run
depend() {
    after $SVC_A
}
start() {
    return 0
}
EOF
chmod 0755 "$INITD/$SVC_B"

slinitctl --system reload-all >/dev/null 2>&1
sleep 1

# Both scripts are loaded but nothing has asked for either yet. If A
# were already up, "B did not pull A in" below would prove nothing.
assert_service_state "$SVC_A" "STOPPED" "$SVC_A STOPPED before anything is started"
assert_service_state "$SVC_B" "STOPPED" "$SVC_B STOPPED before anything is started"

# Start ONLY B. `after` is ordering, not a requirement: B must come up
# on its own and A must stay down. Starting A first (as this case used
# to) cannot tell `after` from `need` — both end with both STARTED.
slinitctl --system start "$SVC_B" >/dev/null 2>&1
wait_for_service "$SVC_B" "STARTED" 10 || true
assert_service_state "$SVC_B" "STARTED" "$SVC_B STARTED on its own (after $SVC_A is advisory)"
assert_service_state "$SVC_A" "STOPPED" "$SVC_A not pulled in by $SVC_B's 'after' (need would)"

# A still starts normally when asked — the script itself is fine.
slinitctl --system start "$SVC_A" >/dev/null 2>&1
wait_for_service "$SVC_A" "STARTED" 10 || true
assert_service_state "$SVC_A" "STARTED" "$SVC_A STARTED when started explicitly"

test_summary
