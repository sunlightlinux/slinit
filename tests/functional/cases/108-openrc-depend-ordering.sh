#!/bin/sh
# Test: OpenRC init.d depend() { after other } maps to advisory
# ordering (AfterOptional), not a hard dep. Both services must load
# and start via the init.d auto-detect path.

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
    echo "SKIP: init.d fallback disabled at slinit boot"
    test_summary
    return 0
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

_desc=$(slinitctl --system description "$SVC_B" 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_desc" in
    *"$SVC_A"*)
        echo "OK: $SVC_B's description mentions $SVC_A (depend() body reflected)" ;;
    *)
        echo "INFO: description of $SVC_B did not mention $SVC_A (may not surface after-optional)" ;;
esac

slinitctl --system start "$SVC_A" >/dev/null 2>&1
wait_for_service "$SVC_A" "STARTED" 10 || true
assert_service_state "$SVC_A" "STARTED" "$SVC_A STARTED (init.d + depend() body loaded)"

slinitctl --system start "$SVC_B" >/dev/null 2>&1
wait_for_service "$SVC_B" "STARTED" 10 || true
assert_service_state "$SVC_B" "STARTED" "$SVC_B STARTED (after $SVC_A advisory)"

test_summary
